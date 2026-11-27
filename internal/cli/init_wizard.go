// Package cli implements the interactive terminal setup wizard for Lagrangian.
// Running configuration inside the terminal avoids exposing an unauthenticated
// web setup page on Linux/remote servers and works cleanly over SSH.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/lagrangian-dev/lagrangian/internal/core"
	"github.com/mattn/go-isatty"
)

var ErrCanceled = errors.New("setup canceled by user")

// IsInteractive reports whether f is attached to a real terminal (TTY).
// Note: /dev/null is also a character device on Unix, so os.ModeCharDevice alone
// is insufficient; isatty accurately detects interactive terminals across OSes.
func IsInteractive(f *os.File) bool {
	if f == nil {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// RunInitWizard guides the user step by step in the terminal to create or
// overwrite config.yaml and store credentials safely in the platform secret store.
func RunInitWizard(svc *core.Service, in io.Reader, out io.Writer, force bool) (core.SetupInitResult, error) {
	status := svc.SetupStatus()
	if status.Initialized && !force {
		return core.SetupInitResult{}, fmt.Errorf("配置文件已存在 (%s)，如需重新配置请添加 --force 参数", status.ConfigPath)
	}

	reader := bufio.NewReader(in)

	fmt.Fprintln(out, "====================================================")
	fmt.Fprintln(out, "  Lagrangian · 终端分步初始化向导")
	fmt.Fprintln(out, "====================================================")
	fmt.Fprintf(out, "当前主机: %s/%s | 凭据后端: %s\n", runtime.GOOS, runtime.GOARCH, status.SecretBackend)
	fmt.Fprintf(out, "目标配置: %s\n\n", status.ConfigPath)

	// Step 1: Deployment mode
	fmt.Fprintln(out, "[Step 1/4] 选择部署模式")
	fmt.Fprintln(out, "  1) 单机模式 (standalone) — 本机一体化运行（默认监听 127.0.0.1，免密使用）")
	fmt.Fprintln(out, "  2) 常驻节点模式 (server) — Linux/macOS/Windows 常驻服务（默认监听 0.0.0.0，开启令牌鉴权）")
	modeChoice, err := prompt(reader, out, "请选择 [1/2]", "1")
	if err != nil {
		return core.SetupInitResult{}, err
	}
	mode := "standalone"
	effectiveHost := "127.0.0.1"
	defaultAuth := false
	switch strings.ToLower(strings.TrimSpace(modeChoice)) {
	case "2", "server":
		mode = "server"
		effectiveHost = "0.0.0.0"
		defaultAuth = true
	}

	// Step 2: Listen port (host derives automatically from mode; --host remains available at runtime)
	fmt.Fprintln(out, "\n[Step 2/4] 配置监听端口")
	fmt.Fprintf(out, "  当前模式自动绑定地址: %s（如需指定特定网卡，可在启动时使用 --host 参数覆盖）\n", effectiveHost)
	portStr, err := prompt(reader, out, "监听端口", strconv.Itoa(status.DefaultConfig.Server.Port))
	if err != nil {
		return core.SetupInitResult{}, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return core.SetupInitResult{}, fmt.Errorf("无效端口号 %q（需为 1-65535）", portStr)
	}

	// Step 3: Data directory
	fmt.Fprintln(out, "\n[Step 3/4] 配置数据目录（存放 SQLite 台账与附件）")
	dataDir, err := prompt(reader, out, "数据目录", status.DefaultConfig.Storage.DataDir)
	if err != nil {
		return core.SetupInitResult{}, err
	}

	// Step 4: Bearer Token auth
	fmt.Fprintln(out, "\n[Step 4/4] 配置 Bearer Token 接口鉴权")
	authEnabled, err := promptYesNo(reader, out, "启用 Bearer Token 接口鉴权", defaultAuth)
	if err != nil {
		return core.SetupInitResult{}, err
	}
	apiToken := ""
	if authEnabled {
		apiToken, err = promptOptional(reader, out, "自定义访问令牌（直接回车自动生成高强度随机令牌）")
		if err != nil {
			return core.SetupInitResult{}, err
		}
	}

	// Summary & confirmation
	fmt.Fprintln(out, "\n------------------ 配置确认预览 ------------------")
	fmt.Fprintf(out, "  部署模式 : %s\n", mode)
	fmt.Fprintf(out, "  监听地址 : %s:%d (由模式自动决定)\n", effectiveHost, port)
	fmt.Fprintf(out, "  数据目录 : %s\n", dataDir)
	fmt.Fprintf(out, "  接口鉴权 : %v\n", authEnabled)
	fmt.Fprintf(out, "  配置路径 : %s\n", status.ConfigPath)
	fmt.Fprintln(out, "--------------------------------------------------")

	confirmed, err := promptYesNo(reader, out, "确认写入配置并初始化数据库", true)
	if err != nil {
		return core.SetupInitResult{}, err
	}
	if !confirmed {
		return core.SetupInitResult{}, ErrCanceled
	}

	res, err := svc.InitSetup(core.SetupInitInput{
		Mode:        mode,
		Port:        port,
		DataDir:     dataDir,
		AuthEnabled: authEnabled,
		APIToken:    apiToken,
	})
	if err != nil {
		return core.SetupInitResult{}, err
	}

	fmt.Fprintln(out, "\n初始化完成！")
	if res.GeneratedToken != "" {
		fmt.Fprintln(out, "====================================================")
		fmt.Fprintln(out, "  已生成访问令牌（仅展示一次，请妥善保存）：")
		fmt.Fprintf(out, "  %s\n", res.GeneratedToken)
		fmt.Fprintln(out, "====================================================")
	}
	return res, nil
}

// PromptStartNow asks whether to launch the server immediately after first-run init.
func PromptStartNow(in io.Reader, out io.Writer) bool {
	reader := bufio.NewReader(in)
	ok, err := promptYesNo(reader, out, "是否立即启动 Lagrangian 服务", true)
	return err == nil && ok
}

func prompt(r *bufio.Reader, out io.Writer, label, defaultVal string) (string, error) {
	fmt.Fprintf(out, "%s [%s]: ", label, defaultVal)
	line, err := r.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return "", err
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return defaultVal, nil
	}
	return trimmed, nil
}

func promptOptional(r *bufio.Reader, out io.Writer, label string) (string, error) {
	fmt.Fprintf(out, "%s: ", label)
	line, err := r.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func promptYesNo(r *bufio.Reader, out io.Writer, label string, defaultYes bool) (bool, error) {
	hint := "y/N"
	if defaultYes {
		hint = "Y/n"
	}
	fmt.Fprintf(out, "%s [%s]: ", label, hint)
	line, err := r.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return defaultYes, nil
	}
}
