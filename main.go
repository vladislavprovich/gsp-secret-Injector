package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	cliTemplate "text/template"

	"github.com/hjson/hjson-go"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"

	"github.com/vladislavprovich/gsp-secret-injector/gcp"
	"github.com/vladislavprovich/gsp-secret-injector/pkg/jsonutil"
	"github.com/vladislavprovich/gsp-secret-injector/pkg/numericutil"
	"github.com/vladislavprovich/gsp-secret-injector/pkg/signal"
	"github.com/vladislavprovich/gsp-secret-injector/pkg/stringutil"
	"github.com/vladislavprovich/gsp-secret-injector/template"
)

const (
	appName                     = "inject"
	exportedOutputFormatter     = `export %s="%s"`
	unexportedOutputFormatter   = `%s="%s"`
	unquotedOutputFormatter     = `%s=%s`
	jsonIndent                  = `    `
	envVarInjectorKeyValue      = "INJECTOR_KEY_VALUE"
	envVarInjectorProject       = "INJECTOR_PROJECT"
	envVarInjectorSecretName    = "INJECTOR_SECRET_NAME"
	envVarInjectorSecretVersion = "INJECTOR_SECRET_VERSION"
)

var (
	// Version contains the current Version.
	Version = "dev"
	// BuildDate contains a string with the build BuildDate.
	BuildDate = "unknown"
	// GitCommit git commit sha
	GitCommit = "dirty"
	// GitBranch git branch
	GitBranch = "dirty"
	// Platform OS/ARCH
	Platform = ""
	// Logger
	log = logrus.New()
)

func main() {
	app := &cli.App{
		Name:                   appName,
		HelpName:               appName,
		Usage:                  "Handle signals and inject environment variables from GCP secret manager.",
		Action:                 run,
		Version:                Version,
		UseShortOptionHandling: true,
		Flags:                  flags(),
	}

	cli.AppHelpTemplate = template.AppHelpTemplate()
	cli.HelpPrinter = func(out io.Writer, templ string, data interface{}) {
		funcMap := cliTemplate.FuncMap{
			"stripDefault": template.StripDefault,
		}
		cli.HelpPrinterCustom(out, templ, data, funcMap)
	}

	cli.VersionPrinter = func(c *cli.Context) {
		fmt.Fprintf(os.Stdout, "version: %s\n", Version)
		fmt.Fprintf(os.Stdout, "  build date: %s\n", BuildDate)
		fmt.Fprintf(os.Stdout, "  commit: %s\n", GitCommit)
		fmt.Fprintf(os.Stdout, "  branch: %s\n", GitBranch)
		fmt.Fprintf(os.Stdout, "  platform: %s\n", Platform)
		fmt.Fprintf(os.Stdout, "  built with: %s\n", runtime.Version())
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

func debug(ctx *cli.Context, writer io.Writer) {
	cli.ShowVersion(ctx)

	for _, flag := range ctx.App.Flags {
		for _, name := range flag.Names() {
			if len(name) == 1 {
				// skip aliases
				continue
			}

			value := ctx.String(name)
			if stringutil.IsBlank(value) {
				value = "<NOT SET>"
			}
			fmt.Fprintf(writer, "%s: %s\n", name, value)
		}
	}

	for i, a := range ctx.Args().Slice() {
		fmt.Fprintf(writer, "  [%d]: %v\n", i, a)
	}
}

func flags() []cli.Flag {
	return []cli.Flag{

		&cli.StringFlag{
			Name:     "key-file",
			Aliases:  []string{"k"},
			Usage:    "Path to file containing JSON format service account key.",
			Required: false,
		},

		&cli.StringFlag{
			Name:     "key-value",
			Aliases:  []string{"K"},
			Usage:    "Base64 encoded string containing JSON format service account key.",
			Required: false,
			EnvVars:  []string{envVarInjectorKeyValue},
		},

		&cli.BoolFlag{
			Name:     "format-shell",
			Aliases:  []string{"e"},
			Usage:    "Parse secret contents and convert to exported shell key/value settings.",
			Required: false,
		},

		&cli.BoolFlag{
			Name:     "format-shell-unexported",
			Aliases:  []string{"u"},
			Usage:    "Parse secret contents and convert to unexported shell key/value settings.",
			Required: false,
		},

		&cli.BoolFlag{
			Name:     "format-json",
			Aliases:  []string{"j"},
			Usage:    "Parse secret contents and convert from hJSON to JSON.",
			Required: false,
		},

		&cli.BoolFlag{
			Name:     "format-raw",
			Aliases:  []string{"r"},
			Usage:    "Output unparsed secret contents. This will likely be hJSON or JSON.",
			Required: false,
		},

		&cli.BoolFlag{
			Name:     "ignore",
			Aliases:  []string{"i"},
			Usage:    "Ignore missing secret options.",
			Required: false,
		},

		&cli.BoolFlag{
			Name:     "ignore-preserve-env",
			Aliases:  []string{"I"},
			Usage:    "Ignore missing secret options, pass environment variables from parent OS into command shell.",
			Required: false,
		},

		&cli.BoolFlag{
			Name:     "preserve-env",
			Aliases:  []string{"E"},
			Usage:    "Pass environment variables from parent OS into command shell.",
			Required: false,
		},

		&cli.StringFlag{
			Name:     "output-file",
			Aliases:  []string{"o"},
			Usage:    `Write output to file. Default is stdout; passing "-" also represents stdout.`,
			Required: false,
		},

		&cli.StringFlag{
			Name:     "project",
			Aliases:  []string{"p"},
			Usage:    "GCP project id.",
			Required: false,
			EnvVars:  []string{envVarInjectorProject},
		},

		&cli.StringFlag{
			Name:     "secret-name",
			Usage:    "Name of secret containing environment variables and values.",
			Aliases:  []string{"S"},
			Required: false,
			EnvVars:  []string{envVarInjectorSecretName},
		},

		&cli.StringFlag{
			Name:     "secret-version",
			Usage:    `Version of secret containing environment variables and values. ("latest" if not specified)`,
			Aliases:  []string{"V"},
			Required: false,
			EnvVars:  []string{envVarInjectorSecretVersion},
		},

		&cli.BoolFlag{
			Name:     "debug",
			Usage:    "Show debug information.",
			Aliases:  []string{"d"},
			Required: false,
		},
	}
}

func hasConflictingOptions(ctx *cli.Context) (bool, error) {
	// Disallow conflicting format options.
	if numericutil.BoolToInt(ctx.Bool("format-shell"))+numericutil.BoolToInt(ctx.Bool("format-shell-unexported"))+
		numericutil.BoolToInt(ctx.Bool("format-json"))+numericutil.BoolToInt(ctx.Bool("format-raw")) > 1 {
		return true, errors.New("multiple output formats are not supported")
	}

	if numericutil.BoolToInt(ctx.Bool("preserve-env"))+numericutil.BoolToInt(ctx.Bool("ignore-preserve-env")) > 1 {
		return true, errors.New("multiple preserve environment options are not supported")
	}

	if numericutil.StringToBoolInt(ctx.String("key-file"))+numericutil.StringToBoolInt(ctx.String("key-value")) > 1 {
		return true, errors.New("multiple key source formats are not supported")
	}

	return false, nil
}

func hasMissingRetrievalOptions(ctx *cli.Context) (bool, error) {
	minimumCount := 3
	if !stringutil.IsBlank(ctx.String("secret-version")) {
		minimumCount++
	}

	actualCount := numericutil.BoolToInt(
		numericutil.StringToBool(ctx.String("key-file")) || numericutil.StringToBool(ctx.String("key-value"))) +
		numericutil.StringToBoolInt(ctx.String("project")) + numericutil.StringToBoolInt(ctx.String("secret-name")) +
		numericutil.StringToBoolInt(ctx.String("secret-version"))

	if actualCount > 0 && actualCount < minimumCount {
		return true, errors.New("missing dependencies for secret retrieval options")
	}

	return false, nil
}

func run(ctx *cli.Context) error {
	var buf bytes.Buffer

	if ctx.Bool("debug") {
		debug(ctx, os.Stdout)
	}

	if bad, err := hasConflictingOptions(ctx); bad {
		return err
	}

	if bad, err := hasMissingRetrievalOptions(ctx); bad {
		return err
	}

	if wantsToPullSecret(ctx) {
		if err := gcp.FetchSecretDocument(ctx, &buf); err != nil && !wantsToIgnorePullSecretFailures(ctx) {
			return err
		}
	}

	outputFile := os.Stdout
	if !stringutil.IsBlank(ctx.String("output-file")) && ctx.String("output-file") != "-" {
		var err error
		outputFile, err = os.Create(ctx.String("output-file"))
		if err != nil {
			return err
		}
		defer func() {
			_ = outputFile.Close()
		}()
	}

	if ctx.Bool("format-json") {
		return outputJSON(ctx, &buf, outputFile)
	} else if ctx.Bool("format-raw") {
		return outputRaw(ctx, &buf, outputFile)
	} else if ctx.Bool("format-shell") {
		return outputShellExported(ctx, &buf, outputFile)
	} else if ctx.Bool("format-shell-unexported") {
		return outputShellUnexported(ctx, &buf, outputFile)
	}

	if err := runCommand(ctx, &buf, ctx.Args().Slice()); err != nil {
		return err
	}

	return nil
}

func runCommand(ctx *cli.Context, buf *bytes.Buffer, commandWithArgs []string) error {
	var command string
	var args []string

	if len(commandWithArgs) == 0 {
		log.Warn("no command specified")
		return nil
	}

	command = commandWithArgs[0]
	if !stringutil.IsBlank(command) && len(commandWithArgs) > 1 {
		args = commandWithArgs[1:]
	}

	cmd := exec.Command(command, args...)
	cmd.Env = []string{}
	if ctx.Bool("preserve-env") || (ctx.Bool("ignore-preserve-env") && buf.Len() == 0) {
		cmd.Env = os.Environ()
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var err error
	var data map[string]interface{}
	if data, err = parseHJSON(ctx, buf); err != nil {
		return err
	}

	var envList []string
	envList, err = convertMapToKeyValueList(ctx, data)
	if err != nil {
		log.WithError(err).Error("failed to resolve secrets")
	}
	cmd.Env = append(cmd.Env, envList...)

	err = cmd.Start()
	if err != nil {
		log.WithError(err).Error("failed to start command")
		return err
	}

	signal.ForwardToPid(cmd.Process.Pid, log, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)

	err = cmd.Wait()
	if err != nil {
		log.WithError(err).Error("failed to wait for command to complete")
		return err
	}

	return nil
}

func convertMapToKeyValueList(ctx *cli.Context, data map[string]interface{}) ([]string, error) {
	if ctx == nil {
		return []string{}, errors.New("invalid context")
	}

	if data == nil {
		return []string{}, errors.New("invalid environment map")
	}

	var jsonBytes []byte
	var err error
	if jsonBytes, err = json.Marshal(data); err != nil {
		return []string{}, err
	}

	return jsonutil.Flatten(jsonBytes, "environment", unquotedOutputFormatter), nil
}

func outputShellExported(ctx *cli.Context, buffer *bytes.Buffer, writer io.Writer) error {
	return outputShell(ctx, buffer, writer, exportedOutputFormatter)
}

func outputShellUnexported(ctx *cli.Context, buffer *bytes.Buffer, writer io.Writer) error {
	return outputShell(ctx, buffer, writer, unexportedOutputFormatter)
}

func outputShell(ctx *cli.Context, buffer *bytes.Buffer, writer io.Writer, formatter string) error {
	if ctx == nil {
		return errors.New("invalid context")
	}

	if buffer == nil {
		return errors.New("invalid buffer")
	}

	var err error
	var data map[string]interface{}
	if data, err = parseHJSON(ctx, buffer); err != nil {
		return err
	}

	var jsonBytes []byte
	if jsonBytes, err = json.Marshal(data); err != nil {
		return err
	}

	list := jsonutil.Flatten(jsonBytes, "environment", formatter)

	for _, v := range list {
		fmt.Fprintf(writer, "%s\n", v)
	}

	return nil
}

func outputJSON(ctx *cli.Context, buffer *bytes.Buffer, writer io.Writer) error {
	if ctx == nil {
		log.Fatal(errors.New("invalid context"))
	}

	if buffer == nil {
		log.Fatal(errors.New("invalid buffer"))
	}

	var err error
	var data map[string]interface{}
	if data, err = parseHJSON(ctx, buffer); err != nil {
		return err
	}

	var prettyJSON []byte
	if prettyJSON, err = json.MarshalIndent(data, "", jsonIndent); err != nil {
		log.Fatal(err)
	}

	prettyJSON = jsonutil.ConvertUnicodeToASCII(prettyJSON)

	n, err := writer.Write(prettyJSON)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	if n != len(prettyJSON) {
		fmt.Println("failed to write data")
		os.Exit(1)
	}

	return nil
}

func outputRaw(ctx *cli.Context, buffer *bytes.Buffer, writer io.Writer) error {
	if ctx == nil {
		return errors.New("invalid context")
	}

	if buffer == nil {
		return errors.New("invalid buffer")
	}

	n, err := writer.Write(buffer.Bytes())
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	if n != len(buffer.Bytes()) {
		fmt.Println("failed to write data")
		os.Exit(1)
	}

	return nil
}

func parseHJSON(ctx *cli.Context, buffer *bytes.Buffer) (map[string]interface{}, error) {
	data := make(map[string]interface{})

	if ctx == nil {
		return data, errors.New("invalid context")
	}

	if buffer == nil {
		return data, errors.New("invalid buffer")
	}

	if err := hjson.Unmarshal(buffer.Bytes(), &data); err != nil {
		log.Fatal(err)
	}

	return data, nil
}

func wantsToPullSecret(ctx *cli.Context) bool {
	// We only need to check if one of the options that would be needed to pull a secret is defined.
	return numericutil.StringToBool(ctx.String("project"))
}

func wantsToIgnorePullSecretFailures(ctx *cli.Context) bool {
	return ctx.Bool("ignore") || ctx.Bool("ignore-preserve-env")
}
