# 无 Mac 的 TrollStore 安装与公网 IP 配对

本文覆盖两件事：在没有 Mac 的机器上做出能装进老系统 iPhone/iPad 的 IPA，以及不用 Tailscale、
按公网 IP 完成手机与电脑配对。默认环境是 Ubuntu 开发机 + 已装 TrollStore 的设备。

## 适用前提

- 设备已安装 TrollStore（TrollStore 2 覆盖 iOS 16.2–16.6.1；iOS 16.3.1 在其范围内）。
- 开发机只有 Linux，没有 Xcode，因此 Swift 编译只能由 GitHub Actions 的 macOS runner 完成。
- 电脑有可入站的公网地址（云主机公网 IP、或路由器端口转发）。家庭宽带没有公网 IPv4 时本方案不适用。

## 0. 先确认 deployment target

`main` 的 `ios/MimiRemote/project.yml` 以 iOS 18.0 为 deployment target，产物 `MinimumOSVersion` 就是 18.0，
iOS 16.x 会被系统直接拒绝安装。需要先合入把 target 下调到 16.2 的改动（分支
`codex/ios16-2-compat`），新系统 API 全部由 `Sources/Core/Compat/` 下的兼容层按 `#available` 收口。

TrollStore 工作流会核对产物：`MinimumOSVersion` 高于填写的手机系统版本就判失败，
不会把装不上的包搬回手机才发现。

## 1. 在 GitHub Actions 上编译未签名 IPA

仓库 → Actions → `iOS TrollStore IPA` → Run workflow：

| 参数 | 取值 |
| --- | --- |
| `configuration` | `Debug`（日常够用）或 `Release` |
| `target_ios_version` | 手机系统版本，例如 `16.2`（16.3.1 的设备填 `16.2` 也可，只要不低于产物要求） |

工作流做三件事：装 Go（工程 preBuild 会调用 `scripts/build-tailcat-mobile.sh` 生成静态
`TailcatMobile.xcframework`）→ `xcodebuild -sdk iphoneos ... CODE_SIGNING_ALLOWED=NO` →
核对产物架构、扩展数量和最低系统要求，再把 `.app` 打成 `Payload/` 结构的 IPA。

产物 artifact 名为 `mimi-trollstore-unsigned-<commit>`，下载后解压得到
`MimiRemote-trollstore-unsigned.ipa`。这个包**没有任何签名**，不能直接安装。

## 2. 在 Ubuntu 上注入 entitlement 并伪签名

```bash
cd ~/Projects/mimi-remote
bash ./scripts/ios-trollstore-package.sh ~/Downloads/MimiRemote-trollstore-unsigned.ipa
```

脚本行为：

1. 解出 `Payload/MimiRemote.app`；
2. 按 bundle id 选择仓库里的 entitlement 来源，剥掉 `aps-environment` 与 `com.apple.developer.*`
   这类需要 Apple 描述文件的键，**保留 App Group**（否则 `UserDefaults(suiteName:)` 返回 nil）；
3. 先签 `Frameworks/` 里的内层 Mach-O（工具链在这里放 Swift 回部署库，未签名的 dylib 会让
   dyld 报 `code signature invalid`，这类库不带 entitlement），再签两个内置扩展（推送、小组件），
   最后签主 App，bundle 一律用 `ldid -S<entitlements>`；
4. 清掉可能残留的 `_CodeSignature` 目录，重新 zip 成 `MimiRemote-<版本>-trollstore.ipa`，打印 SHA-256。

依赖 `python3`、`unzip`、`zip`、`ldid`。确认本机 TrollStore 会注入描述文件时，可用
`TROLLSTORE_KEEP_PROFILE_ENTITLEMENTS=1` 保留推送 entitlement；主 App 的 entitlement 来源可用
`TROLLSTORE_APP_ENTITLEMENTS` 覆盖。

## 3. 安装到手机

把 IPA 传到手机（iCloud Drive、Files 直连、或手机浏览器下载都行），在 Files 里选择用 TrollStore 打开并 Install。

- 与 App Store / TestFlight 版是同一个 bundle id，安装会覆盖，登录态与 Keychain 项可能随之丢失。
- 安装成功但启动即闪退，先怀疑 entitlement：确认没有保留 `aps-environment`。

## 4. 电脑侧开启公网直连

```bash
agentd public --enabled=true --advertise <公网IP或域名>
agentd restart
agentd public --status
```

- 公网入口是独立的 TLS 监听，默认 `0.0.0.0:8443`；明文 `8787` 仍只对本机、局域网和 Tailscale 开放，
  公网侧永远只有 HTTPS，访问码不会被明文窃听。
- `--advertise` 写手机实际要连的地址。NAT、弹性 IP、CDN 后面必须显式写明，不能靠自动探测。
- 路由器或云安全组要把外部 8443 转发/放行到主机 8443。
- `agentd public --status` 会打印证书指纹（`base64(SHA-256(证书 DER))`）。证书重新生成后指纹会变，
  手机必须重新扫码。

## 5. 扫码配对与指纹校验

```bash
agentd pair --network public
```

`auto` 会优先给 Tailscale 或局域网地址，公网直连必须显式写 `--network public`。二维码里的
`endpoint` 是 `https://<公网地址>:8443`，并带 `tls_pin`（证书指纹），是指纹进入手机的唯一入口。

手机 App 的行为：

1. 扫码后先登记指纹，再发起 `claim`；
2. `agentd` 回显当前指纹，与二维码里的不一致就报「电脑证书已变化」并中止配对；
3. 之后每个公网请求都在 TLS 挑战里比对指纹，再把证书链锚定到那张被指纹确认的证书上，
   继续由系统校验有效期与主机名；任一环节不通过直接断开（fail-closed）。

因此：

- **手动输入公网 https 地址连不上**：没有带外指纹就退回系统信任链，自签证书验不过。必须扫码。
- 删除连接档案即撤销信任，同一公网 IP 换另一台电脑不会被旧指纹锁死。
- 局域网与 Tailscale 配对不带指纹，行为不变，也不会因为电脑同时开了公网而报错。

## 6. 已知限制

| 项 | 结果 |
| --- | --- |
| 锁屏审批推送 | TrollStore 安装没有描述文件，`aps-environment` 已剥离 → APNs 不可用；App 前台/后台的状态刷新不受影响 |
| 小组件与 Live Activity | App Group 保留，能读到主 App 写入的数据；TrollStore 应用的小组件在个别系统版本上添加受限 |
| 证书信任 | 只信任配对时那张证书；换证书、换公网地址都要重新扫码 |
| 明文 HTTP | 公网入口不提供明文，Token 不会在公网明文传输 |
| 验证边界 | Swift 只能在 macOS runner 上编译，Linux 本机没有 Xcode，产物行为以真机安装结果为准 |

## 7. 回滚

手机上用 TrollStore 卸载即可。电脑侧关闭公网入口：

```bash
agentd public --enabled=false
agentd restart
agentd public --status
```
