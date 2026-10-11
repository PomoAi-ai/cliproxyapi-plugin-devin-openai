# PLAN -- Quota and reasoning capabilities

## Status: done
## Task: 003
## Related: 002
## Baseline Commit: a486556

## Goal
Expose accurate account quota through CPA and preserve supported reasoning/model variants when clients use native Claude Messages.

## Non-goals
Do not change legacy accounts, bypass gateway permissions, or infer undocumented gateway balances/capabilities. No paid model requests or tests in CI.

## Acceptance Criteria
- CPA recognizes the plugin quota capability; failures never appear as zero or unlimited balances.
- Quota source and scope are explicit, separate from AI Gateway authorization.
- Reasoning controls follow evidence from the gateway contract and CPA capabilities.
- Real host mock verification includes Claude Messages streaming, tools, and reasoning behavior.

## Decisions
- Use dev workflow, Ponytail Full, and focused exploration subagents.
- Work in the independent repository; private operational evidence remains outside this repository.
- Exploration and architecture completed by quota and model/translation subagents; no unresolved design fork.
- Quota uses the existing Devin seat-management unary protobuf contract through host.http.do; report daily/weekly remaining fractions and reset times, not gateway-specific credits or dollars. Only session-token credentials are supported.
- Use protowire for validated protobuf parsing; require user/plan structures, interpret omitted scalar percentages according to protobuf default zero, reject malformed or out-of-range values.
- Gateway catalog does not advertise reasoning limits. Set UserDefined on models so CPA preserves explicit reasoning intent rather than declaring models non-reasoning; do not invent ThinkingSupport levels or budgets.
- Preserve variant UID routing and display names. Native Claude clients use CPA Messages translation, not an unverified upstream Anthropic endpoint. Unsigned thinking history is not losslessly preserved by CPA v8.0.23.

## Implementation Map
| # | File | Intent | Depends | Done |
|---|------|--------|---------|------|
| 1 | quota.go, quota_test.go, go.mod, go.sum | Quota protocol and failure boundaries | none | yes |
| 2 | plugin.go, plugin_test.go | Quota dispatch and honest model capabilities | none | yes |
| 3 | tools/verify_host.py | Quota and Claude streaming/reasoning integration | 1,2 | yes |
| 4 | README.md, THIRD_PARTY_NOTICES.md, .github/workflows/build.yml | Explain scope and ship dependency license | 1,2 | yes |

## Validation
| Command | Required | Done |
|---------|----------|------|
| go test ./... and go vet ./... | yes | yes |
| Real CPA host mock integration | yes | yes |
| Independent code review | yes | yes |

## Results
- go test, go test -race and go vet passed; macOS race linker emitted a non-fatal LC_DYSYMTAB warning.
- Official CPA v8.0.23 host passed 17 mock inference requests and the quota flow, including quota auth selection, 75%/0% values, resets, unsupported service keys, Messages reasoning SSE and tools.
- Independent reviews approved quota and model capability changes. The dependency BSD license is included in binary artifacts.
- Native gateway acceptance and full Anthropic thinking signatures are not established by mock tests; the plugin does not claim them.
