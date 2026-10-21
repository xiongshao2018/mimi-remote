#!/usr/bin/env bash
set -euo pipefail

# 在 Linux 上把 CI（.github/workflows/ios-trollstore-ipa.yml）产出的未签名 IPA
# 变成 TrollStore 可安装的 IPA。
#
# CI 侧只做 CODE_SIGNING_ALLOWED=NO 的真机编译，不带任何签名。这里用 ldid 给
# 主 App 和内置扩展注入 entitlement 并伪签名：
#   - App Group 必须留在签名里，否则 UserDefaults(suiteName:) 返回 nil；
#   - aps-environment 这类需要 Apple 描述文件的 entitlement 会让进程起不来，
#     TrollStore 没有描述文件，默认剥掉（推送随之不可用，见安装文档）。
#
# 用法：
#   bash ./scripts/ios-trollstore-package.sh <未签名 IPA 或 .app 目录> [输出 IPA]
# 环境变量：
#   TROLLSTORE_APP_ENTITLEMENTS        覆盖主 App 的 entitlement 来源
#   TROLLSTORE_KEEP_PROFILE_ENTITLEMENTS=1
#                                     保留 aps-environment 等需要描述文件的键
#                                     （只在确认 TrollStore 会注入描述文件时使用）

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

APP_ENTITLEMENTS="${TROLLSTORE_APP_ENTITLEMENTS:-$REPO_ROOT/ios/MimiRemote/Resources/MimiRemote.entitlements}"
NOTIFICATION_ENTITLEMENTS="$REPO_ROOT/ios/MimiRemote/NotificationServiceExtension/MimiNotificationService.entitlements"
WIDGET_ENTITLEMENTS="$REPO_ROOT/ios/MimiRemote/WidgetExtension/MimiCarStatusWidget.entitlements"

TEMPORARY_DIR=""
cleanup() {
  [[ -z "$TEMPORARY_DIR" ]] || rm -rf "$TEMPORARY_DIR"
}
trap cleanup EXIT

fail() {
  echo "TrollStore 打包失败：$1" >&2
  exit 1
}

require_command() {
  local command_name="$1"
  command -v "$command_name" >/dev/null 2>&1 \
    || fail "缺少命令 $command_name。TrollStore 打包需要 python3、unzip、zip 和 ldid。"
}

usage() {
  sed -n '2,26p' "${BASH_SOURCE[0]}" | sed -n 's/^# \{0,1\}//p'
}

case "${1:-}" in
  -h|--help|help)
    usage
    exit 0
    ;;
esac

for command_name in python3 unzip zip ldid; do
  require_command "$command_name"
done
[[ $# -ge 1 ]] || { usage >&2; fail "缺少输入路径"; }

input_path="$1"
output_path="${2:-}"
[[ -e "$input_path" ]] || fail "输入不存在：$input_path"
input_path="$(cd "$(dirname "$input_path")" && printf '%s/%s' "$(pwd)" "$(basename "$input_path")")"

for entitlements_file in "$APP_ENTITLEMENTS" "$NOTIFICATION_ENTITLEMENTS" "$WIDGET_ENTITLEMENTS"; do
  [[ -f "$entitlements_file" ]] || fail "缺少 entitlement 文件：$entitlements_file"
done

TEMPORARY_DIR="$(mktemp -d "${TMPDIR:-/tmp}/mimi-trollstore.XXXXXX")"
# stage 目录是 zip 的工作目录：IPA 根必须正好是 Payload/，不能多一层临时目录。
stage_dir="$TEMPORARY_DIR/stage"
payload_dir="$stage_dir/Payload"
mkdir -p "$payload_dir"

if [[ -d "$input_path" && "$input_path" == *.app ]]; then
  cp -a "$input_path" "$payload_dir/"
else
  unpack_dir="$TEMPORARY_DIR/unpacked"
  mkdir -p "$unpack_dir"
  unzip -q "$input_path" -d "$unpack_dir" || fail "解压失败：$input_path"
  app_bundle="$(find "$unpack_dir" -maxdepth 2 -type d -name '*.app' | sort | head -n 1)"
  [[ -n "$app_bundle" ]] || fail "压缩包里找不到 .app：$input_path"
  cp -a "$app_bundle" "$payload_dir/"
fi

app_bundle="$payload_dir/$(basename "$(find "$payload_dir" -maxdepth 1 -type d -name '*.app' | head -n 1)")"
[[ -d "$app_bundle" ]] || fail "找不到主 App：$payload_dir"

# plist 读写只用 python3 的 plistlib：本机没有 plutil，也不依赖 macOS 工具。
plist_print() {
  python3 - "$1" "$2" <<'PY'
import plistlib
import sys

with open(sys.argv[1], "rb") as handle:
    value = plistlib.load(handle)
for key in sys.argv[2].split("."):
    if not isinstance(value, dict) or key not in value:
        sys.exit(1)
    value = value[key]
print(value if isinstance(value, str) else "")
PY
}

# 剥掉需要 Apple 描述文件的 entitlement。TrollStore 安装的应用没有描述文件，
# 保留这些键会让 launchd 直接拒绝启动；App Group 不在此列，必须保留。
filter_entitlements() {
  python3 - "$1" "$2" "${TROLLSTORE_KEEP_PROFILE_ENTITLEMENTS:-0}" <<'PY'
import plistlib
import sys

deny_exact = {"aps-environment"}
deny_prefix = ("com.apple.developer.",)
keep = sys.argv[3] == "1"

with open(sys.argv[1], "rb") as handle:
    entitlements = plistlib.load(handle)
assert isinstance(entitlements, dict), "entitlements 必须是字典"

if not keep:
    removed = []
    for key in list(entitlements):
        if key in deny_exact or key.startswith(deny_prefix):
            entitlements.pop(key)
            removed.append(key)
    if removed:
        print("stripped:" + ",".join(sorted(removed)), file=sys.stderr)

with open(sys.argv[2], "wb") as handle:
    plistlib.dump(entitlements, handle)
PY
}

entitlements_for_bundle() {
  local bundle_id="$1"
  case "$bundle_id" in
    com.gaixianggeng.mimi.notificationservice) printf '%s\n' "$NOTIFICATION_ENTITLEMENTS" ;;
    com.gaixianggeng.mimi.carstatuswidget) printf '%s\n' "$WIDGET_ENTITLEMENTS" ;;
    *) printf '%s\n' "$APP_ENTITLEMENTS" ;;
  esac
}

# Frameworks/ 里的 Mach-O 单独签：工具链会在这里放 Swift 回部署库
# （例如 libswift_Concurrency.dylib），未签名的 dylib 会让 dyld 在启动时报
# "code signature invalid"。这类库不需要 entitlement，用 ldid -S 直接签。
sign_nested_macho() {
  local target="$1"
  local executable=""
  if [[ -d "$target" ]]; then
    local framework_name
    framework_name="$(basename "$target")"
    if [[ -f "$target/Info.plist" ]]; then
      local info_executable
      info_executable="$(plist_print "$target/Info.plist" CFBundleExecutable || true)"
      [[ -n "$info_executable" ]] && executable="$target/$info_executable"
    fi
    [[ -x "$executable" ]] || executable="$target/${framework_name%.framework}"
  else
    executable="$target"
  fi
  if [[ ! -x "$executable" ]]; then
    printf '  跳过（找不到可执行文件）%s\n' "$target"
    return 0
  fi

  rm -rf "$target/_CodeSignature"
  ldid -S "$executable" || fail "ldid 签名失败：$executable"
  local cdhash
  cdhash="$(ldid -h "$executable")" || fail "签名校验失败：$executable"
  [[ -n "$cdhash" ]] || fail "签名后读不到 CDHash：$executable"
  printf '  已签名 %-46s CDHash=%s\n' "$(basename "$target")" "${cdhash:0:16}"
}

sign_nested_machos_in() {
  local bundle="$1"
  local frameworks_dir="$bundle/Frameworks"
  [[ -d "$frameworks_dir" ]] || return 0
  while IFS= read -r nested; do
    sign_nested_macho "$nested"
  done < <(
    find "$frameworks_dir" -maxdepth 1 \
      \( \( -type d -name '*.framework' \) -o \( -type f -name '*.dylib' \) \) | sort
  )
}

sign_bundle() {
  local bundle="$1"
  local info_plist="$bundle/Info.plist"
  local bundle_id executable entitlements filtered
  [[ -f "$info_plist" ]] || fail "bundle 缺少 Info.plist：$bundle"
  bundle_id="$(plist_print "$info_plist" CFBundleIdentifier)" \
    || fail "读不到 CFBundleIdentifier：$info_plist"
  executable="$(plist_print "$info_plist" CFBundleExecutable)" \
    || fail "读不到 CFBundleExecutable：$info_plist"
  [[ -f "$bundle/$executable" ]] || fail "缺少可执行文件：$bundle/$executable"

  entitlements="$(entitlements_for_bundle "$bundle_id")"
  filtered="$TEMPORARY_DIR/entitlements/$(basename "$bundle").plist"
  mkdir -p "$TEMPORARY_DIR/entitlements"
  filter_entitlements "$entitlements" "$filtered" || fail "生成 entitlements 失败：$bundle"

  # 先签内层再签外层：外层签名的 CDHash 覆盖不到内层，顺序反了不会报错但会漏签。
  sign_nested_machos_in "$bundle"

  # 旧签名目录会和 ldid 写在 Mach-O 内的签名并存，TrollStore 会读到过期校验信息。
  rm -rf "$bundle/_CodeSignature"
  ldid -S"$filtered" "$bundle/$executable" || fail "ldid 签名失败：$bundle/$executable"
  local cdhash
  cdhash="$(ldid -h "$bundle/$executable")" || fail "签名校验失败：$bundle/$executable"
  [[ -n "$cdhash" ]] || fail "签名后读不到 CDHash：$bundle/$executable"
  printf '  已签名 %-46s CDHash=%s\n' "$bundle_id" "${cdhash:0:16}"
}

printf '主 App: %s\n' "$app_bundle"
if [[ -d "$app_bundle/PlugIns" ]]; then
  # 先签扩展再签主 App：TrollStore 按 bundle 逐个校验，顺序错了不会报错但容易漏签。
  while IFS= read -r appex; do
    sign_bundle "$appex"
  done < <(find "$app_bundle/PlugIns" -maxdepth 1 -type d -name '*.appex' | sort)
fi
sign_bundle "$app_bundle"

short_version="$(plist_print "$app_bundle/Info.plist" CFBundleShortVersionString || printf '0')"
minimum_os="$(plist_print "$app_bundle/Info.plist" MinimumOSVersion || printf 'unknown')"

if [[ -z "$output_path" ]]; then
  output_path="$PWD/MimiRemote-${short_version}-trollstore.ipa"
fi
case "$output_path" in
  /*) ;;
  *) output_path="$PWD/$output_path" ;;
esac
rm -f "$output_path"
(
  cd "$stage_dir"
  zip -qry "$output_path" Payload
) || fail "重打包失败：$output_path"

checksum=""
if command -v sha256sum >/dev/null 2>&1; then
  checksum="$(sha256sum "$output_path" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
  checksum="$(shasum -a 256 "$output_path" | awk '{ print $1 }')"
fi

printf '\n产物: %s\n' "$output_path"
[[ -n "$checksum" ]] && printf 'SHA-256: %s\n' "$checksum"
printf '版本: %s，最低系统要求: %s\n' "$short_version" "$minimum_os"
printf '下一步: 把该 IPA 传到手机，用 TrollStore 打开并 Install。\n'
