package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/mritd/logrus"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var conf TPClashConf

var build string
var commit string
var version string
var clash string

var rootCmd = &cobra.Command{
	Use:   "tpclash",
	Short: "Transparent proxy tool for Clash",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Printf("%s\nVersion: %s\nBuild: %s\nClash Core: %s\nCommit: %s\n\n", logo, version, build, clash, commit)

		if conf.PrintVersion {
			return
		}

		var err error
		if conf.Debug {
			logrus.SetLevel(logrus.DebugLevel)
		}

		logrus.Info("[main] starting tpclash...")

		// Initialize signal control Context
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		defer cancel()

		// Configure Sysctl
		Sysctl()

		// Extract Clash executable and built-in configuration files
		ExtractFiles(&conf)

		// Watch config file
		updateCh := WatchConfig(ctx, &conf)

		// Wait for the first config to return
		clashConfStr := <-updateCh

		// Check clash config
		if _, err = CheckConfig(clashConfStr); err != nil {
			logrus.Fatal(err)
		}

		// Copy remote or local clash config file to internal path
		clashConfPath := filepath.Join(conf.ClashHome, InternalConfigName)
		if err = os.WriteFile(clashConfPath, []byte(clashConfStr), 0644); err != nil {
			logrus.Fatalf("[main] failed to copy clash config: %v", err)
		}

		// Create child process
		clashBinPath := filepath.Join(conf.ClashHome, InternalClashBinName)
		clashUIPath := filepath.Join(conf.ClashHome, conf.ClashUI)
		cmd := exec.Command(clashBinPath, "-f", clashConfPath, "-d", conf.ClashHome, "-ext-ui", clashUIPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{
			AmbientCaps: []uintptr{CAP_NET_BIND_SERVICE, CAP_NET_ADMIN, CAP_NET_RAW},
		}
		logrus.Infof("[main] running cmds: %v", cmd.Args)

		if err = cmd.Start(); err != nil {
			logrus.Fatalf("[main] failed to start clash process: %v: %v", err, cmd.Args)
			cancel()
		}
		if cmd.Process == nil {
			cancel()
			logrus.Fatalf("[main] failed to start clash process: %v", cmd.Args)
		}

		if err = EnableDockerCompatible(); err != nil {
			logrus.Errorf("[main] failed enable docker compatible: %v", err)
		}

		// Watch clash config changes, and automatically reload the config
		go AutoReload(updateCh, clashConfPath)

		logrus.Info("[main] 🍄 提莫队长正在待命...")
		if conf.Test {
			logrus.Warn("[main] test mode enabled, tpclash will automatically exit after 5 minutes...")
			go func() {
				<-time.After(5 * time.Minute)
				cancel()
			}()
		}
		<-ctx.Done()

		logrus.Info("[main] 🛑 TPClash 正在停止...")
		if err = DisableDockerCompatible(); err != nil {
			logrus.Errorf("[main] failed disable docker compatible: %v", err)
		}

		if cmd.Process != nil {
			if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
				logrus.Error(err)
			}
		}

		logrus.Info("[main] 🛑 TPClash 已关闭!")
	},
}

var encCmd = &cobra.Command{
	Use:   "enc FILENAME",
	Short: "Encrypt config file",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			_ = cmd.Help()
			return
		}
		if conf.ConfigEncPassword == "" {
			logrus.Fatalf("[enc] configuration file encryption password cannot be empty")
		}

		plaintext, err := os.ReadFile(args[0])
		if err != nil {
			logrus.Fatalf("[enc] failed to read config file: %v", err)
		}

		ciphertext := Encrypt(plaintext, conf.ConfigEncPassword)
		if err = os.WriteFile(args[0]+".enc", ciphertext, 0644); err != nil {
			logrus.Fatalf("[enc] failed to write encrypted config file: %v", err)
		}

		logrus.Infof("[enc] encrypted file storage location %s", args[0]+".enc")
	},
}

var decCmd = &cobra.Command{
	Use:   "dec FILENAME",
	Short: "Decrypt config file",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			_ = cmd.Help()
			return
		}
		if conf.ConfigEncPassword == "" {
			logrus.Fatalf("[dec] configuration file encryption password cannot be empty")
		}

		ciphertext, err := os.ReadFile(args[0])
		if err != nil {
			logrus.Fatalf("[dec] failed to read encrypted config file: %v", err)
		}

		plaintext, err := Decrypt(ciphertext, conf.ConfigEncPassword)
		if err != nil {
			logrus.Fatalf("[dec] failed to decrypt config file: %v", err)
		}

		if err = os.WriteFile(strings.TrimSuffix(args[0], ".enc"), plaintext, 0644); err != nil {
			logrus.Fatalf("[dec] failde to write config file: %v", err)
		}

		logrus.Infof("[enc] decrypted file storage location %s", strings.TrimSuffix(args[0], ".enc"))
	},
}

func init() {
	cobra.EnableCommandSorting = false

	rootCmd.AddCommand(encCmd, decCmd)

	rootCmd.PersistentFlags().BoolVar(&conf.Debug, "debug", false, "enable debug log")
	rootCmd.PersistentFlags().BoolVar(&conf.Test, "test", false, "enable test mode, tpclash will automatically exit after 5 minutes")
	rootCmd.PersistentFlags().StringVarP(&conf.ClashHome, "home", "d", "/data/clash", "clash home dir")
	rootCmd.PersistentFlags().StringVarP(&conf.ClashConfig, "config", "c", "/etc/clash.yaml", "clash config local path or remote url")
	rootCmd.PersistentFlags().StringVarP(&conf.ClashUI, "ui", "u", "yacd", "clash dashboard(official|yacd)")
	rootCmd.PersistentFlags().DurationVarP(&conf.CheckInterval, "check-interval", "i", 120*time.Second, "remote config check interval")
	rootCmd.PersistentFlags().StringSliceVar(&conf.HttpHeader, "http-header", []string{}, "http header when requesting a remote config(key=value)")
	rootCmd.PersistentFlags().DurationVar(&conf.HttpTimeout, "http-timeout", 10*time.Second, "http request timeout when requesting a remote config")
	rootCmd.PersistentFlags().StringVar(&conf.ConfigEncPassword, "config-password", "", "the password for encrypting the config file")
	rootCmd.PersistentFlags().BoolVar(&conf.DisableExtract, "disable-extract", false, "disable extract files")
	rootCmd.PersistentFlags().BoolVarP(&conf.PrintVersion, "version", "v", false, "version for tpclash")
}

func main() {
	cobra.CheckErr(rootCmd.Execute())
}
