package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/gaixianggeng/mimi-remote/internal/config"
	agentsetup "github.com/gaixianggeng/mimi-remote/internal/setup"
)

// runPublic 管理「公网 IP 直连」模块。它不依赖 Tailscale：手机从任意网络按公网
// 地址连进来，传输信任由 HTTPS + 配对时带外下发的证书指纹提供。
func runPublic(args []string) error { return runPublicWithWriters(args, os.Stdout, os.Stderr) }

func runPublicWithWriters(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("public", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", config.DefaultPath(), "配置文件路径")
	enabled := fs.Bool("enabled", false, "是否允许手机从公网 IP 直连（HTTPS）")
	listen := fs.String("listen", "", "公网 TLS 绑定地址，例如 0.0.0.0:8443；留空使用默认端口")
	advertise := fs.String("advertise", "", "手机要连的公网地址，可带端口；NAT 或弹性 IP 必须显式写明")
	termination := fs.String("tls-termination", "", "HTTPS 由谁终结：self_signed（默认，agentd 自签并监听公网端口）或 tunnel（隧道终结，本机不监听公网端口，证书由隧道提供商签发）")
	restore := fs.String("restore-state", "", "恢复先前公网设置的事务结果 JSON")
	statusOnly := fs.Bool("status", false, "只查询当前公网入口，不修改配置")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	mode, count := "", 0
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "enabled":
			mode = "enabled"
			count++
		case "status":
			// --status=false 只是显式写回默认值，不算选择了查询模式。
			if statusOnly != nil && f.Value.String() == "true" {
				mode = "status"
				count++
			}
		case "restore-state":
			mode = "restore"
			count++
		}
	})
	if count != 1 {
		return fmt.Errorf("必须且只能传入 --enabled、--status 或 --restore-state 之一")
	}
	if mode == "status" {
		state, err := agentsetup.PublicAccessStatus(*path)
		if err != nil {
			return err
		}
		return printPublicAccessState(stdout, *asJSON, state)
	}
	if err := prepareDefaultConfigMigration(fs, *path, stderr); err != nil {
		return err
	}
	var result agentsetup.PublicAccessConfigurationResult
	var err error
	if mode == "restore" {
		var previous agentsetup.PublicAccessConfigurationResult
		if err = json.Unmarshal([]byte(*restore), &previous); err != nil {
			return err
		}
		if previous.Previous != nil && previous.Previous.Enabled {
			if err := ensurePlatformPublicAccessAllowed(); err != nil {
				return err
			}
		}
		result, err = agentsetup.RestorePublicAccess(*path, previous)
	} else {
		if *enabled {
			if err := ensurePlatformPublicAccessAllowed(); err != nil {
				return err
			}
		}
		result, err = agentsetup.ConfigurePublicAccess(*path, *enabled, *listen, *advertise, *termination)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSONTo(stdout, result)
	}
	fmt.Fprintf(stdout, "公网直连：%t，TLS 终结方式 %s\n", result.Enabled, result.TLSTermination)
	if result.Listen != "" {
		fmt.Fprintf(stdout, "本机公网监听：%s\n", result.Listen)
	} else {
		fmt.Fprintln(stdout, "本机不监听公网端口：由隧道客户端把公网域名回源到本机明文端口")
	}
	if result.Advertise != "" {
		fmt.Fprintf(stdout, "公网地址：%s\n", result.Advertise)
	}
	if result.Endpoint != "" {
		fmt.Fprintf(stdout, "手机连接地址：%s\n", result.Endpoint)
	}
	if result.Fingerprint != "" {
		fmt.Fprintf(stdout, "公网证书指纹(SHA-256, base64)：%s\n", result.Fingerprint)
	}
	if result.RestartRequired {
		fmt.Fprintln(stdout, "配置已保存，重启 agentd 后生效。")
	}
	return nil
}

func printPublicAccessState(stdout io.Writer, asJSON bool, state agentsetup.PublicAccessState) error {
	if asJSON {
		return printJSONTo(stdout, state)
	}
	if !state.Enabled {
		fmt.Fprintln(stdout, "公网直连：关闭")
		fmt.Fprintln(stdout, "  开启：agentd public --enabled=true --advertise <公网IP>")
		fmt.Fprintln(stdout, "  隧道：agentd public --enabled=true --advertise <隧道域名> --tls-termination=tunnel")
		return nil
	}
	if state.TLSTermination == config.PublicTLSTerminationTunnel {
		fmt.Fprintln(stdout, "公网直连：开启，TLS 由隧道终结（本机不监听公网端口，证书由隧道提供商签发）")
	} else {
		fmt.Fprintf(stdout, "公网直连：开启，绑定 %s\n", state.Listen)
	}
	if state.Advertise != "" {
		fmt.Fprintf(stdout, "公网地址：%s\n", state.Advertise)
	}
	if state.Endpoint == "" {
		fmt.Fprintln(stdout, "  尚未确定公网地址：agentd public --advertise <公网IP或域名>")
		return nil
	}
	fmt.Fprintf(stdout, "手机连接地址：%s\n", state.Endpoint)
	if state.TLSTermination == config.PublicTLSTerminationTunnel {
		fmt.Fprintln(stdout, "  证书由隧道提供商签发，手机按系统信任链校验，二维码不带证书指纹")
		return nil
	}
	if state.Fingerprint == "" {
		fmt.Fprintln(stdout, "  证书尚未生成：重启 agentd 或运行 agentd pair 后出现指纹")
		return nil
	}
	fmt.Fprintf(stdout, "公网证书指纹(SHA-256, base64)：%s\n", state.Fingerprint)
	return nil
}
