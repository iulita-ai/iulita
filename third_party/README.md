# Fosite JOSE migration

`fosite/` is an owned copy of **canonical Ory Fosite v0.49.0**, not a new
official Fosite release. It retains all upstream Go source, tests, generated
mocks and the Apache-2.0 license. Upstream website, images and Node/build tooling
are omitted; the Go suites do not use them.

Canonical provenance:

- Module: `github.com/ory/fosite v0.49.0`
- Commit: `653c812bc40cbda049857b432ad3346f1a53d42d`
- Go module checksum: `h1:KNqO7RVt/1X8F08/UI0Y+GRvcpscCWgjqvpLBQPRovo=`
- [Canonical release](https://github.com/ory/fosite/releases/tag/v0.49.0)

`fosite.patch` is the complete migration difference from that checksummed source
selection after four deterministic public-fixture annotations in the verifier.
Those annotations mark the known-invalid password, public PEM key, query test
expression and RFC documentation example;
the parsed PEM bytes and test behavior remain unchanged.
`python3 scripts/check_fosite.py verify` independently downloads the canonical
module, applies the patch and compares every file, including tests and license.
Review changes to the patch and source together. `regenerate` updates the patch
after an intentional source change; it is never run automatically by CI.

## Why this copy exists

Fosite v0.49.0 depends on go-jose v3, which has no fixed release for
[GHSA-mgcc-cww5-fp2q](https://github.com/go-jose/go-jose/security/advisories/GHSA-mgcc-cww5-fp2q).
The copy uses **official `github.com/go-jose/go-jose/v4 v4.1.9`**, including its
upstream security fix and default 4096-bit RSA JWK parsing limit. We do not raise
that bound or suppress the advisory. Iulita's RSA signing keys are 3072 bits.

The bounded migration changes JOSE import paths, supplies explicit parsing
algorithms, uses v4 JWT serialization, and preserves symmetric/Ed25519 key slice
forms during verification. Parsing `none` retains Fosite's existing explicit
unsafe sentinel and client-policy checks; it does not authorize unsigned tokens.
Two refresh error format strings and one upstream test's obsolete 512-bit RSA
fixture are adjusted for the current Go toolchain. The source otherwise retains
v0.49.0 behavior. Existing Iulita request/JWK admission restrictions remain.

Reference implementation and additional security tests:
[`mrchypark/fosite@06741a29e6f11260e9d6fe45222eedc00cb38959`](https://github.com/mrchypark/fosite/commit/06741a29e6f11260e9d6fe45222eedc00cb38959),
independently compared with canonical v0.49.0. Its error-message assertions are
adapted to v4.1.9's typed oversized-RSA error without skipping rejection checks.
Iulita does not download or build that fork as a dependency. The canonical public
PEM test fixture is explicitly marked for secret scanning (its decoded bytes are
unchanged); the other three public examples have exact inline annotations as well.
No project-wide scanner exclusions are added.

## Verification and maintenance

Root `go test ./...` excludes nested modules. CI explicitly runs:

```sh
python3 scripts/check_fosite.py verify
python3 scripts/check_fosite.py test
python3 scripts/check_fosite.py race
python3 scripts/check_fosite.py upgrade
python3 scripts/check_fosite.py graph
```

The complete normal/race Fosite suites use Iulita's selected versions for application-declared
shared modules and upstream's declared versions for test-only dependencies.
Unused transitive module-graph metadata can differ when upstream test dependencies
are activated; the post-test version check covers all root-declared shared modules. A temporary
module file leaves the checked-in modules unchanged. The upgrade check builds
old and new test binaries, issues synthetic credentials with canonical
Fosite/JOSE v3, closes the store, then resumes code/PKCE, RS256 access and refresh
sessions using the patched version and identical persisted keys/state. It also
checks code replay and refresh reuse revocation. No real accounts are used.

Both the main module and `experiments/mobileoauth` use this copy. All command
package closures and shipped image inventories must exclude JOSE v3 and identify
official v4.1.9. An unused JOSE v3 requirement inherited from `ory/x` may remain
visible in `go mod graph` or `go list -m all`; that metadata is distinct from
linked packages and binary/SBOM evidence.

Remove the local replacements and this directory when an official compatible
Fosite release uses fixed JOSE. Require the same full normal/race, upgrade,
application OAuth and exact image checks first. Preserve issuer, keys and current
database state during upgrades or rollback; rolling back code must not restore
stale refresh-token state. Never replace JOSE module majors blindly.
