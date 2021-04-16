package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"time"

	"gopkg.in/yaml.v2"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/version"
)

var (
	showVersion   = flag.Bool("version", false, "Print version information.")
	configFile    = flag.String("config.file", "script-exporter.yml", "Script exporter configuration file.")
	listenAddress = flag.String("web.listen-address", ":9172", "The address to listen on for HTTP requests.")
	metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	shell         = flag.String("config.shell", "/bin/sh", "Shell to execute script")
	// A regex pattern that only matches valid ASCII domain name characters to
	// prevent inadvertent or malicious injection of special shell characters
	// into the scripts environment.
	targetRegexp = regexp.MustCompile("^[a-zA-Z0-9-.]{4,253}$")
)

type Config struct {
	Scripts []*Script `yaml:"scripts"`
}

type Script struct {
	Name    string `yaml:"name"`
	Content string `yaml:"script"`
	Timeout int64  `yaml:"timeout"`
}

type Measurement struct {
	Script   *Script
	Success  int
	ExitCode int
	Duration float64
}

func runScript(script *Script, target string) (err error, rc int) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(script.Timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, *shell)
	cmd.Env = append(os.Environ(), fmt.Sprintf("TARGET=%s", target))

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err, 1
	}

	if _, err = stdin.Write([]byte(script.Content)); err != nil {
		return err, 1
	}
	stdin.Close()

	if err = cmd.Run(); err != nil {
		exitError := err.(*exec.ExitError)
		rc = exitError.Sys().(syscall.WaitStatus).ExitStatus()
	} else {
		rc = cmd.ProcessState.Sys().(syscall.WaitStatus).ExitStatus()
	}

	return err, rc
}

func runScripts(scripts []*Script, target string) []*Measurement {
	measurements := make([]*Measurement, 0)

	ch := make(chan *Measurement)

	for _, script := range scripts {
		go func(script *Script) {
			start := time.Now()
			success := 0
			err, rc := runScript(script, target)
			duration := time.Since(start).Seconds()

			if err == nil {
				log.Debugf("OK: %s to %s (after %fs).", script.Name, target, duration)
				success = 1
			} else {
				log.Infof("ERROR: %s to %s: %s (failed after %fs).", script.Name, target, err, duration)
			}

			ch <- &Measurement{
				Script:   script,
				Duration: duration,
				Success:  success,
				ExitCode: rc,
			}
		}(script)
	}

	for i := 0; i < len(scripts); i++ {
		measurements = append(measurements, <-ch)
	}

	return measurements
}

func scriptFilter(scripts []*Script, name, pattern string) (filteredScripts []*Script, err error) {
	if name == "" && pattern == "" {
		err = errors.New("`name` or `pattern` required")
		return
	}

	var patternRegexp *regexp.Regexp

	if pattern != "" {
		patternRegexp, err = regexp.Compile(pattern)

		if err != nil {
			return
		}
	}

	for _, script := range scripts {
		if script.Name == name || (pattern != "" && patternRegexp.MatchString(script.Name)) {
			filteredScripts = append(filteredScripts, script)
		}
	}

	return
}

func scriptRunHandler(w http.ResponseWriter, r *http.Request, config *Config) {
	params := r.URL.Query()
	name := params.Get("name")
	pattern := params.Get("pattern")
	target := params.Get("target")

	scripts, err := scriptFilter(config.Scripts, name, pattern)

	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// If the passed target does not validate return an error.
	if target != "" && !targetRegexp.MatchString(target) {
		log.Infof("ERROR: Target %s failed to match targetRegexp", target)
		http.Error(w, "Invalid target parameter", 400)
		return
	}

	measurements := runScripts(scripts, target)

	for _, measurement := range measurements {
		fmt.Fprintf(w, "script_duration_seconds{script=\"%s\"} %f\n", measurement.Script.Name, measurement.Duration)
		fmt.Fprintf(w, "script_success{script=\"%s\"} %d\n", measurement.Script.Name, measurement.Success)
		fmt.Fprintf(w, "script_exit_code{script=\"%s\"} %d\n", measurement.Script.Name, measurement.ExitCode)
	}
}

func init() {
	prometheus.MustRegister(version.NewCollector("script_exporter"))
}

func main() {
	flag.Parse()

	if *showVersion {
		fmt.Fprintln(os.Stdout, version.Print("script_exporter"))
		os.Exit(0)
	}

	log.Infoln("Starting script_exporter", version.Info())

	yamlFile, err := ioutil.ReadFile(*configFile)

	if err != nil {
		log.Fatalf("Error reading config file: %s", err)
	}

	config := Config{}

	err = yaml.Unmarshal(yamlFile, &config)

	if err != nil {
		log.Fatalf("Error parsing config file: %s", err)
	}

	log.Infof("Loaded %d script configurations", len(config.Scripts))

	for _, script := range config.Scripts {
		if script.Timeout == 0 {
			script.Timeout = 15
		}
	}

	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/probe", func(w http.ResponseWriter, r *http.Request) {
		scriptRunHandler(w, r, &config)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
			<head><title>Script Exporter</title></head>
			<body>
			<h1>Script Exporter</h1>
			<p><a href="` + *metricsPath + `">Metrics</a></p>
			</body>
			</html>`))
	})

	log.Infoln("Listening on", *listenAddress)

	if err := http.ListenAndServe(*listenAddress, nil); err != nil {
		log.Fatalf("Error starting HTTP server: %s", err)
	}
}
