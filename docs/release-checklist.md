# Release Checklist

| Field | Value |
| --- | --- |
| Target | `v1.0.0` |
| Status | Defined in Phase 0; nothing below is complete or released |
| Related | [Requirements](requirements.md), [Testing](testing-and-quality.md), [Security](security.md), [Persistence](persistence.md) |

A release is acceptable only when every applicable item is checked with recorded
evidence. A compile step or a green unit-test job alone is never sufficient.
Phase 0 does not publish a release, tag `v1.0.0`, or configure automatic
publication.

## 1. Release Preconditions

- [ ] Phase 6 is accepted with all functional and reliability evidence.
- [ ] All required CI checks pass on the release commit.
- [ ] No unresolved critical or high-severity security finding.
- [ ] CodeQL, `govulncheck`, `gosec`, dependency review, image scan, and secret
      scanning have been run and recorded.
- [ ] The project owner has explicitly authorized the release.
- [ ] Documentation matches the release behavior; no planned feature is
      advertised as implemented.
- [ ] License and ownership metadata are correct.
- [ ] A rollback/withdrawal procedure is documented and tested in dry-run form.

## 2. Functional Requirements

### Commands and output

- [ ] `init`, `validate`, `check`, `monitor`, `status`, `history`, and `version`
      are implemented and match the CLI contract.
- [ ] Root/help behavior, unknown commands, and unknown flags are tested.
- [ ] Human output is readable and does not corrupt JSON mode.
- [ ] `--output json` emits valid JSON on stdout and diagnostics on stderr.
- [ ] `monitor --output json` emits valid NDJSON.
- [ ] Exit codes `0`, `1`, `2`, `3`, and `4` are tested for every applicable
      command.
- [ ] Graceful SIGINT/SIGTERM behavior and forced signal codes are verified.
- [ ] `version` metadata is correct in release binaries.

### Monitoring and incidents

- [ ] Down threshold of 1 and greater than 1 is verified.
- [ ] Sub-threshold failures do not create incidents or alerts.
- [ ] A confirmed Down creates exactly one open incident and one Down event.
- [ ] Repeated failures do not duplicate Down.
- [ ] Recovery threshold of 1 and greater than 1 is verified.
- [ ] Recovery closes exactly one incident and emits one Recovery event.
- [ ] One open incident per backend is enforced under concurrent probes.
- [ ] Status codes and body matching (`contains`, regex) behave as specified.
- [ ] Response-size limits and malformed responses fail safely.
- [ ] Network errors, DNS failures, and timeouts produce failed observations, not
      internal crashes.
- [ ] TLS verification failure is handled with a safe failure and is never
      bypassed.
- [ ] Redirect policy and cross-origin credential protection are verified.
- [ ] Private-network monitoring works according to the documented trust model.

### Notifications

- [ ] Generic webhook payloads match schema version 1.
- [ ] Down, Reminder, and Recovery event types are emitted under the correct
      policy.
- [ ] Reminder interval, cap, suppression, missed-slot behavior, and cancellation
      on recovery are verified.
- [ ] `Idempotency-Key` equals the event ID.
- [ ] `2xx` success, `3xx` failure, retryable `408`/`425`/`429`/`5xx`, permanent
      `4xx`, timeouts, and `Retry-After` are tested.
- [ ] Retry exhaustion produces a dead event without corrupting incident state.
- [ ] Webhook delivery failure and later recovery are verified.
- [ ] No secret, header value, backend URL, webhook URL, or response body appears
      in any payload.
- [ ] Consumers can deduplicate duplicates caused by the documented crash
      window.

### Restart and persistence

- [ ] Restart while a backend is down does not duplicate the Down alert.
- [ ] Restart after recovery during downtime emits one Recovery based on actual
      observed health.
- [ ] Open incidents, failure/recovery streaks, reminder schedule, and pending
      events survive restart.
- [ ] Stale `delivering` events are reset and retried with the same event ID.
- [ ] Partial transaction failure leaves no partial incident state.
- [ ] First-run schema creation and forward migrations are verified.
- [ ] A database newer than the binary fails closed with upgrade guidance.
- [ ] A corrupt database is not overwritten or silently recreated.
- [ ] Retention preserves open incidents, pending events, and current state.
- [ ] Orphaned incidents are visible and are not auto-closed or reminded.
- [ ] SQLite file permissions and single-process assumptions are documented and
      verified.
- [ ] Backup and restore procedure is documented and exercised.

## 3. Quality Evidence

- [ ] `gofmt` check passes.
- [ ] `go vet ./...` passes.
- [ ] `golangci-lint run ./...` passes with the pinned version.
- [ ] Unit tests pass.
- [ ] Integration tests pass.
- [ ] End-to-end tests pass.
- [ ] `go test -race ./...` passes.
- [ ] Coverage is at least 80% overall and in each core package.
- [ ] `govulncheck ./...` has no unresolved reachable vulnerability. Any accepted
      finding has a documented rationale and owner.
- [ ] CodeQL has no unresolved high/critical findings.
- [ ] `gosec` results are reviewed.
- [ ] Test suites are deterministic and do not require production services.
- [ ] Flaky tests are fixed or quarantined with an issue; they are not retried
      silently.
- [ ] CI logs and evidence are archived/linked.

## 4. Security Review

- [ ] Threat model reviewed against the released feature set.
- [ ] Secret resolution and redaction tested across logs, errors, JSON, history,
      and webhook payloads.
- [ ] Header injection and reserved-header validation tested.
- [ ] URL validation, userinfo rejection, query redaction, and redirect policy
      tested.
- [ ] Response-size and header-size limits tested.
- [ ] SSRF trust model and deployment guidance reviewed; any new restriction is
      documented.
- [ ] TLS verification cannot be disabled.
- [ ] Webhook payloads contain no secrets or sensitive URLs.
- [ ] Docker image contains no secrets, config, database, shell, or package
      manager.
- [ ] Container runs as non-root with a read-only root filesystem and dropped
      capabilities in the documented configuration.
- [ ] Config file and SQLite permissions are verified.
- [ ] CI workflows use least-privilege permissions and no repository secrets in
      pull-request jobs.
- [ ] Repository secret scanning is clean.
- [ ] Dependencies are reviewed, licenses are compatible, and the SBOM is
      generated.
- [ ] Image and artifact vulnerability scans are clean or explicitly accepted.
- [ ] A security review record is signed off by the maintainer.

## 5. Platform Matrix

Compilation is required for every target, and runtime validation is required for
every target advertised as supported.

| Target | Compile | Runtime validation |
| --- | --- | --- |
| Linux `amd64` | [ ] | [ ] |
| Linux `arm64` | [ ] | [ ] |
| Windows `amd64` | [ ] | [ ] |
| macOS `amd64` | [ ] | [ ] |
| macOS `arm64` | [ ] | [ ] |

Runtime validation per target:

- [ ] `version` text and JSON.
- [ ] `validate` against a valid and invalid config.
- [ ] `check` against a local controlled HTTP server.
- [ ] SQLite creation and persistence.
- [ ] JSON output and at least exit codes `0`, `1`, and `2`.
- [ ] `monitor` startup and graceful shutdown where the platform supports the
      signal.
- [ ] No target is advertised if only cross-compilation was performed.

Artifact requirements:

- [ ] Version, commit, and build date are embedded correctly.
- [ ] `-trimpath` and CGO-disabled static builds are used.
- [ ] SHA-256 checksums are published.
- [ ] Signatures/provenance are published if the approved release design includes
      them.
- [ ] Artifact names and archive layout are documented.
- [ ] No debug paths, local usernames, or secrets are embedded.

## 6. Docker Release

- [ ] Multi-stage build uses the pinned Go toolchain.
- [ ] Runtime image is scratch or an equally minimal reviewed base.
- [ ] Binary is static and runs without a shell.
- [ ] Runs as numeric non-root UID/GID `65532:65532`.
- [ ] CA certificates are present.
- [ ] Config is mounted read-only; no config is baked into the image.
- [ ] `/data` is explicitly mounted; container writable layer is not used for
      durable data.
- [ ] SQLite data survives container removal/recreation with the named volume.
- [ ] `--read-only`, `--cap-drop=ALL`, and
      `--security-opt=no-new-privileges` are validated.
- [ ] SIGTERM produces graceful shutdown.
- [ ] Image tags are immutable/digest-pinned for release; `latest` alone is not a
      release reference.
- [ ] Multi-architecture image availability matches the supported matrix.
- [ ] Image scan and SBOM are recorded.
- [ ] `docker run ... version` and a monitored run are smoke-tested.

## 7. Documentation

- [ ] README explains the shipped product, not planned features.
- [ ] Installation for standalone binaries and Docker is accurate.
- [ ] Configuration reference matches the implementation and defaults.
- [ ] CLI reference includes flags, output, and exit codes.
- [ ] Incident/notification behavior and at-least-once semantics are documented.
- [ ] Persistence, backup, restore, and retention are documented.
- [ ] Security and secret-handling guidance is complete.
- [ ] Docker persistence and non-root volume permissions are explained.
- [ ] Examples contain no real secrets or production endpoints.
- [ ] Limitations and non-goals are current.
- [ ] Contribution workflow and code of conduct/license references are current.
- [ ] Changelog and release notes are complete.
- [ ] Upgrade notes cover schema/config compatibility.

## 8. Reproducibility and Release Procedure

### Reproducibility

- [ ] A clean checkout builds with the documented Go version and commands.
- [ ] Toolchain, linter, and release-tool versions are pinned.
- [ ] `-trimpath` is used; build paths are not embedded.
- [ ] Build date is derived from a reproducible source such as
      `SOURCE_DATE_EPOCH`.
- [ ] Two clean builds produce identical checksums, or any non-reproducible field
      is documented and justified.
- [ ] Container base images are pinned by digest for release.
- [ ] Dependency versions are locked by `go.mod`/`go.sum` and the module proxy is
      documented.
- [ ] Release workflow runs from a protected tag, not arbitrary input.
- [ ] Checksums, SBOM, and provenance are attached to the release.

### Procedure

1. [ ] Freeze changes on a release branch; confirm the version and changelog.
2. [ ] Run the full local and CI verification suite on the release commit.
3. [ ] Create a signed tag `v1.0.0` after owner approval.
4. [ ] Build artifacts from the tag with the pinned toolchain.
5. [ ] Verify checksums, metadata, and platform smoke tests.
6. [ ] Build and scan the multi-arch image; publish by digest.
7. [ ] Publish release notes and artifact checksums.
8. [ ] Install from published artifacts in a clean environment and run a smoke
       check.
9. [ ] Record all evidence links in the release checklist.
10. [ ] Announce only after all gates are verified.

### Withdrawal/rollback

- [ ] A faulty release is marked as withdrawn in release notes.
- [ ] Artifacts are retained for investigation but not recommended.
- [ ] Docker tags are corrected; immutable digests are not rewritten.
- [ ] A patch release fixes the issue.
- [ ] Root cause and user impact are documented.

## 9. Sign-Off

| Role | Name | Evidence |
| --- | --- | --- |
| Maintainer | Erfan Abouei | |
| Security review | | |
| Release verification | | |
| Date | | |

## 10. Phase 0 Status

None of the release gates above is claimed complete. Phase 0 only defines them.
The repository has no `v1.0.0` tag, no published release, and no release
publication workflow.
