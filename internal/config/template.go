package config

// templateYAML is the starter configuration written by init. It must remain
// valid against the schema with no environment variables set, so the webhook
// transport is commented out and every notification policy stays disabled.
const templateYAML = `# AboueiChecker configuration - schema version 1.
#
# Validate this file with:
#   abouei-checker validate --config <path>
#
# Relative paths resolve against the directory containing this file.
# Secrets should use ${NAME} environment references, never literal values.

version: 1

storage:
  database_path: ./data/abouei.db

runtime:
  max_concurrent_probes: 8
  shutdown_timeout: 30s

logging:
  level: info
  format: text

defaults:
  interval: 60s
  timeout: 10s
  failure_threshold: 3
  recovery_threshold: 1
  max_response_bytes: 1048576
  follow_redirects: false
  notifications:
    enabled: false
    notify_on_down: true
    notify_on_recovery: true
    reminder_interval: 0s
    reminder_max: 0

# Uncomment after configuring the webhook and enabling notifications on at
# least one backend. URL and header values may use ${NAME} references.
# notifications:
#   webhook:
#     url: ${ABOUEI_WEBHOOK_URL}
#     timeout: 10s
#     headers:
#       Authorization: "Bearer ${ABOUEI_WEBHOOK_TOKEN}"
#   retry:
#     max_attempts: 5
#     initial_backoff: 5s
#     max_backoff: 5m

retention:
  observations: 720h
  incidents: 4320h
  delivered_events: 168h

backends:
  - id: example
    name: Example backend
    url: https://example.com/health
    accepted_status_codes: [200]
    notifications:
      enabled: false
`

// Template returns a starter configuration that is valid with no environment
// variables set.
func Template() []byte {
	return []byte(templateYAML)
}
