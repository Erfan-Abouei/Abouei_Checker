# ADR-0006: YAML Decoder, Configuration Schema Freeze, and Loader Boundaries

- **Status:** Accepted
- **Date:** 2026-10-22
- **Phase:** 2
- **Related:** [Configuration](../configuration.md), [Security](../security.md), [CLI contract](../cli-contract.md), [ADR-0001](0001-modular-layout-and-dependencies.md), [ADR-0002](0002-configuration-and-persistence-boundaries.md), [ADR-0004](0004-secret-resolution-and-redaction.md)

## Context

Phase 0 defined the canonical version-1 YAML schema and planned
`go.yaml.in/yaml/v3`, but deliberately did not add the dependency or implement a
loader. Phase 2 must freeze the schema, choose a maintained decoder, and settle
details the contract left to implementation: exact length limits, symlink
behavior, expansion scope, error aggregation, and initialization semantics.

Configuration is untrusted input for parsing purposes even though it is
operator-controlled. Parsing must be bounded, deterministic, strict, and free of
network or database side effects. The loader must not expose partially validated
configuration to later phases.

## Decision

### Decoder and dependency

Use `go.yaml.in/yaml/v3` v3.0.5, the maintained go-yaml v3 module, as the only
new direct dependency. It provides:

- strict struct decoding with unknown-field rejection (`Decoder.KnownFields`);
- duplicate mapping-key rejection by default, including nested mappings;
- line information in decode errors;
- YAML anchors/aliases and merge-key support with alias-expansion limits;
- a small transitive footprint: no runtime dependencies beyond the standard
  library.

The dependency is added in the phase where it is first used, as required by
ADR-0001. `gopkg.in/yaml.v3` is not used because the module has moved to the
`go.yaml.in` home; a similarly named package is not substituted.

### Schema freeze

Version `1` is frozen as documented in [configuration.md](../configuration.md).
The approved fields are implemented exactly, including:

- `version`, `storage`, `runtime`, `logging`, `defaults`, `notifications`,
  `retention`, and `backends`;
- per-backend policy inheritance with field-level merge only for
  `notifications`;
- explicit `body_match: null` to clear an inherited matcher;
- `accepted_status_codes` omission inheriting the defaults and an explicit list
  replacing it; `[]` remains invalid;
- required webhook URL when any effective notification policy is enabled.

No new user-facing fields are introduced. Fields for later phases are validated
and normalized but have no runtime effect until their phase.

### Loader boundaries

The loader is split into independent, testable stages:

1. discovery (`Discover`) with `--config` > `ABOUEI_CONFIG` > `./abouei.yaml` >
   user configuration directory and no fallback from explicit sources;
2. bounded regular-file read (1 MiB) with `Lstat` symlink rejection;
3. document pre-checks for empty input, empty documents, multiple documents, and
   mapping root;
4. strict decoding with unknown-field and duplicate-key rejection;
5. one-pass `${NAME}` expansion with `$$` escaping, applied to string-typed
   fields including duration strings, not mapping keys or numeric/boolean fields;
6. validation and normalization into an immutable `Config` value.

Resolved environment values are inserted verbatim and never rescanned. They are
registered with the redaction boundary and are never rendered in validation,
CLI, or diagnostic output. Error messages drop raw YAML scalar snippets and
internal Go type names.

### Limits and file handling

- Configuration files: 1 MiB.
- URLs: 2048 bytes.
- Headers: 50 per target, 8 KiB per value, 16 KiB total header material.
- Backends: 100.
- Detailed validation errors: 20 displayed, with a count of the remainder.
- Each individual threshold, duration, retention, and retry range follows the
  canonical table.
- Symbolic links are not followed for loading. `init` rejects symlink and
  non-regular targets. This is a deliberately narrow behavior: operators point
  at the real file instead of relying on link resolution.
- `init` creates new files with `0600` and parent directories with `0700` where
  supported, refuses overwrite without `--force`, and never creates a database.

## Consequences

Positive:

- Strict, bounded parsing has explicit tests and no hidden fallback behavior.
- Error output remains safe when values are malformed or contain secrets.
- The schema is stable for Phase 3 consumers, which can rely on normalized
  effective policy values.
- Discovery and relative-path behavior are deterministic.
- No duplicate configuration store or premature validation framework is added.

Negative and mitigations:

- Rejecting symlinks may surprise operators who use linked configuration files.
  Pointing `--config` at the real file is documented and tested.
- String-field expansion includes durations and log settings, so a substitution
  such as `${LOG_LEVEL}` is possible. Every substituted value is registered for
  redaction, which can over-redact common values. Dedicated secret variables are
  recommended.
- Duplicate-key detection is delegated to the decoder rather than a custom YAML
  walker. Tests pin the behavior so a dependency change cannot silently relax it.
- The schema freeze means future fields require a schema version change and an
  ADR; this is intentional.

## Alternatives Considered

- **`gopkg.in/yaml.v3`:** rejected because the project is maintained under the
  `go.yaml.in/yaml/v3` module path.
- **`sigs.k8s.io/yaml`:** rejected because it converts through JSON, weakening
  duplicate-key and unknown-field diagnostics.
- **Viper or a configuration framework:** rejected as disproportionate and prone
  to implicit precedence/merging behavior.
- **JSON configuration:** rejected by the approved product decision and poorer
  operator ergonomics for comments.
- **Custom YAML parser or node-only decoder:** rejected; the selected decoder
  already provides strict typed decoding and duplicate-key rejection.
- **Following symlinks:** rejected to keep file handling narrow and predictable.
- **Expanding only header values:** rejected because the approved contract allows
  references in any string field; the narrower implementation would silently
  diverge from the documentation.
