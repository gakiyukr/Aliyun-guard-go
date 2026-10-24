#!/bin/sh
# Aliyun Guard 心跳看門狗（shell 版，取代 Python 版 watchdog.py，語意對齊）。
# 由 aliyun-guard-watchdog.timer 每分鐘呼叫一次：
#   - heartbeat.json 超過 watchdog.timeout_seconds（預設 600，下限 120）未更新 → stale
#   - 連續 watchdog.failure_threshold（預設 2，下限 1）次 stale → 重啟服務 + Telegram 通知一次
#   - 恢復正常後發 Telegram 報平安
# 依賴：jq、curl、systemctl。
set -u

APP_DIR=${ALIYUN_GUARD_HOME:-/opt/aliyun-guard}
CONFIG=${ALIYUN_GUARD_CONFIG:-$APP_DIR/config.json}
HEARTBEAT=${ALIYUN_GUARD_HEARTBEAT:-$APP_DIR/heartbeat.json}
STATE=${ALIYUN_GUARD_WATCHDOG_STATE:-$APP_DIR/watchdog-state.json}
SERVICE=aliyun-guard

checked_at=$(date -Iseconds 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%S+00:00)

cfg() { jq -r "$1" "$CONFIG" 2>/dev/null; }

read_state() { jq -r "$1 // $2" "$STATE" 2>/dev/null || printf '%s' "$2"; }

# 原子更新狀態檔：無既有檔時以 patch 為初始內容
write_state() {
  jq --argjson p "$1" '. + $p' "$STATE" > "$STATE.tmp" 2>/dev/null ||
    printf '%s\n' "$1" > "$STATE.tmp"
  chmod 600 "$STATE.tmp" && mv "$STATE.tmp" "$STATE"
}

disabled_result() {
  write_state "{\"checked_at\":\"$checked_at\",\"failed_checks\":0,\"outage_notified\":false,\"last_status\":\"disabled\",\"disabled_reason\":\"$1\"}"
  printf '{"status":"disabled","reason":"%s","failed_checks":0}\n' "$1"
  exit 0
}

# Telegram 通知：沿用 config.json 的 telegram 設定（bot_token/chat_id/api_base_url）
notify() {
  token=$(cfg '.telegram.bot_token // ""')
  chat=$(cfg '.telegram.chat_id // ""')
  base=$(cfg '.telegram.api_base_url // "https://api.telegram.org"')
  case "$token" in ''|null) return 0 ;; esac
  case "$chat" in ''|null) return 0 ;; esac
  curl -fsS --max-time 20 -o /dev/null "$base/bot$token/sendMessage" \
    --data-urlencode "chat_id=$chat" \
    --data-urlencode "text=$1"
}

[ -r "$CONFIG" ] || { echo "看门狗检查失败: config.json 不可读" >&2; exit 1; }

enabled=$(cfg '.watchdog.enabled // true')
[ "$enabled" = "true" ] || disabled_result watchdog_disabled
[ -f "$APP_DIR/disabled" ] && disabled_result service_paused

valid=$(cfg '[.users[]? | select((.ak // "") != "" and (.sk // "") != "" and (.region // "") != "" and (.instance_id // "") != "")] | length > 0')
[ "$valid" = "true" ] || disabled_result no_valid_instances

timeout=$(cfg '.watchdog.timeout_seconds // 600')
case "$timeout" in ''|*[!0-9]*) timeout=600 ;; esac
[ "$timeout" -lt 120 ] && timeout=120
threshold=$(cfg '.watchdog.failure_threshold // 2')
case "$threshold" in ''|*[!0-9]*) threshold=2 ;; esac
[ "$threshold" -lt 1 ] && threshold=1

now=$(date +%s)
# 心跳年齡：檔案缺失或 JSON 損壞視為 None（stale，對齊 Python 版 age is None）
if [ -r "$HEARTBEAT" ] && hb=$(jq -r '.epoch // 0' "$HEARTBEAT" 2>/dev/null) &&
   case "$hb" in ''|*[!0-9]*) false ;; *) [ "$hb" -gt 0 ] ;; esac; then
  age=$((now - hb))
else
  age=""
fi
stale=0
if [ -z "$age" ] || [ "$age" -gt "$timeout" ]; then
  stale=1
fi

failed=$(read_state '.failed_checks' '0')
case "$failed" in ''|*[!0-9]*) failed=0 ;; esac
outage_notified=$(read_state '.outage_notified' 'false')
[ "$outage_notified" = "true" ] || outage_notified=false
last_hb=$(jq -r '.at // empty' "$HEARTBEAT" 2>/dev/null)

if [ "$stale" = "1" ]; then
  failed=$((failed + 1))
  restart_attempted=false
  restart_ok=null
  notification_error=null
  if [ "$failed" -ge "$threshold" ]; then
    restart_attempted=true
    if systemctl restart "$SERVICE.service" 2>/dev/null; then
      restart_ok=true
    else
      restart_ok=false
    fi
    if [ "$outage_notified" != "true" ]; then
      if [ -n "$age" ]; then age_text="已失联 ${age} 秒"; else age_text="尚未生成心跳"; fi
      if ! notify "Aliyun Guard 监控失联
时间: $(date '+%Y-%m-%d %H:%M:%S')
状态: $age_text
连续检查失败: $failed 次
自动重启: $(case "$restart_ok" in true) echo 成功 ;; *) echo 未成功或不支持 ;; esac)"; then
        notification_error='"telegram notify failed"'
      fi
      outage_notified=true
    fi
  fi
  last_status="stale"
  [ "$failed" -ge "$threshold" ] && last_status="outage"
  write_state "{\"checked_at\":\"$checked_at\",\"failed_checks\":$failed,\"outage_notified\":$outage_notified,\"last_status\":\"$last_status\"}"
  age_json=${age:+$age}
  printf '{"status": "%s", "heartbeat_age_seconds": %s, "failed_checks": %s, "last_heartbeat": "%s", "restart_attempted": %s, "restart_ok": %s, "notification_error": %s}\n' \
    "$last_status" "${age_json:-null}" "$failed" "$last_hb" "$restart_attempted" "$restart_ok" "$notification_error"
  exit 0
fi

notification_error=null
status=healthy
if [ "$outage_notified" = "true" ]; then
  if notify "Aliyun Guard 监控已恢复
时间: $(date '+%Y-%m-%d %H:%M:%S')
当前心跳延迟: ${age} 秒"; then
    status=recovered
  else
    notification_error='"telegram notify failed"'
  fi
fi
failed=0
outage_notified=false
write_state "{\"checked_at\":\"$checked_at\",\"failed_checks\":0,\"outage_notified\":false,\"last_status\":\"$status\"}"
printf '{"status": "%s", "heartbeat_age_seconds": %s, "failed_checks": 0, "last_heartbeat": "%s", "restart_attempted": false, "restart_ok": null, "notification_error": %s}\n' \
  "$status" "${age:-null}" "$last_hb" "$notification_error"
