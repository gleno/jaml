# JAML normal improvement cycle — 2026-10-26

## Scope and execution

- Domain: JAML-owned library code and tests. Three chunks: sandbox lifecycle/recovery/budgets; document parsing/evaluation/lazy decoding; values/wire/isolation boundaries.
- Mode: **normal**. No time or chunk limit; all planned chunks were visited and the pipeline drained.
- Start: 2026-10-26 16:08:35 Europe/Tallinn (14:08:35 UTC). Final verification completed at 17:03:57 Europe/Tallinn (15:03:57 UTC).
- Integration checkout: `/Users/gleb/published/jaml`, `main`, initially clean at `0e75a7cfc4aa4df5221a034ab8ccd2fd597549aa`.
- Requested concurrency: five workers; effective concurrency: three workers plus coordinator, limited by runtime capacity. Hunters were reused as executors and reviewers.
- Working evidence: `.git/codex-improve/2026-10-26-160835-jaml/`, including ledger, findings, issues, proposals and test logs.
- Managed worktree creation could not use the desktop task's non-repository parent directory. Three explicitly owned sibling Git worktrees provided isolation instead. Existing work and ignored earlier reports were preserved. No push, PR, deployment or tracker writes were made.

Discovery used recent-change and concurrency reviews around attribution/recovery, contracts and errors/edges reviews around decoding, and state/data plus boundary reviews around input/wire/freezing. Boundary follow-through found no additional confirmed defects after the cyclic-input finding. Final review inspected the exact landed diffs and shared seams.

## Landed changes

| Commit | Stories | Root cause and resulting behavior |
| --- | --- | --- |
| `2cfe99e2fd1351e639dece87af741b1f615dbe87` | DATA-1 | UTF-8 validation treated maps and slices as trees while only tracking pointer cycles. One active-ancestry guard now covers all three reference kinds. Cyclic data reaches JSON's existing InvalidInput rejection instead of exponential traversal. Slice length remains part of identity so overlapping views cannot hide invalid text. |
| `1cb5a38ba642a8d18c23676a64dfb86092449a73` | REC-1 / LC-Q1, LC-1, LC-2 | Replay fields mixed caller presence, background retention, work cancellation and console completion. One replay lifetime now owns the exclusive child, covers rebuild as well as serving, and joins its watchdog before classification. Live callers are not truncated by abandoned-caller policy; cancelled console callers drain accepted output without waiting for background attribution. |
| `0c665da731cfb8c5525a2b20c50e029d2f78f390` | REC-2 | Sharing availability was mistaken for child existence. An explicit exclusive-child state keeps known Health metrics visible during recovery and preserves orderly retirement, replacement and stale-notification handling. |
| `4522c4cbd2ca61f36c554f0eb1786f2108bf5029` | DOC-2 | Pointer stripping ran before type-cycle detection. Recursive pointer-only targets now return InvalidTarget before evaluation. Ordinary recursive containers and custom decoders remain supported. |
| `d149a6a693ca4a1b5d0a29bf25bceb24c6269a9c` | DOC-1 | A historical list of lazy paths was applied after JSON had already replaced containers. JSON now allocates placeholder pointers and owns which survive; binding only fills existing pointers. Clearing/replacing maps, pointers, array tails or colliding keys no longer panics or resurrects overwritten handles. |

Seven confirmed defects were retired across five coherent local fix commits. LC-Q1 was the same finding as REC-1, not an additional defect.

## Design decisions and rejected directions

- Retention is effective only after the caller departs, with the deadline measured from exclusive admission. A caller leaving after the allowance expires is dropped immediately. The option documentation and ReplayDropped wording support this scope; the original admission test's live-context fixture was corrected to match its abandoned-caller premise.
- The replay deadline retires only the owned child. Passing individual caller cancellation through shared residency compilation was rejected because it could disrupt other callers' builds. Timeout-driven retirement reports ReplayDropped once and is not misattributed as a new job-caused death.
- Health uses the existing availability union with an exclusive state. A separate health-child pointer was rejected because it would need to remain synchronized with dispatcher state.
- A custom lazy-container decoder was rejected during the feel step: it would duplicate encoding/json's error accumulation, offsets, custom decoder side effects, embedded pointer allocation and merge rules. Adding Expression.UnmarshalJSON was also rejected because it changes promoted methods for embedding types.
- The accepted lazy design retains **one encoding/json decode**. `_Encoded("{}  ")` placeholders occupy the same four bytes as the former `null`, preserving error offsets. On failed decode, a shape-guided walk clears unbound placeholders in the final topology, without replaying source paths, invoking custom key decoders, allocating missing parents or entering custom-decoder subtrees.
- Wire protocol version advances from 25 to 26. Its fields are unchanged, but old children emit null where the new parent requires an allocated placeholder; mixed executables must be rejected rather than silently losing handles.
- Direct fixes for host traversal/type hangs target their first wrong link and retain behavioral regression tests. No cosmetic refactors or new dependencies were introduced.

## Regression proofs and reviews

Each fixed story had a failing regression before implementation and a passing result after it. The text-walk regression used a bounded four-level traversal, exposing 15 visits instead of one without leaving runaway work. Recursive pointer inspection used a child process with a ten-second deadline and capped stack. Replay tests shortened the retention allowance and controlled child execution/rebuilding. Health tests seeded metrics and exercised normal retirement, unexpected death, replacement and stale notifications.

Lazy tests cover nil-map panic, cleared pointers, replaced map entries, array tail clearing, slice/struct merges, generated null, integer and stable TextUnmarshaler key collisions, embedded map-value fields, custom decoders, `,string`, and exact JSON error fields. Review caught two regressions before landing: unbound placeholders in partial error results and shifted error offsets. Final-topology cleanup and equal-width placeholders fixed both; complete partial results and original error structs now match the old null representation in both engines.

Independent reviews found no remaining introduced defects in the cycle guard, replay/console ownership, exclusive-child transitions, target normalization or final placeholder implementation. Integration trees were compared to the corresponding tested worker trees after squash merges.

## Verification

| Check | Result |
| --- | --- |
| Initial macOS ordinary suite | PASS, 161.678s |
| Initial macOS race suite | PASS, 484.569s |
| Initial and executor go vet | PASS |
| Input-cycle full ordinary gate | PASS, 159.684s |
| Recursive-target full ordinary gate | PASS, 160.102s |
| Replay/health full ordinary gate | PASS, 164.529s |
| Replay/health focused race gate | PASS, 4.333s |
| Final decoding full ordinary gate before rebase | PASS, 161.547s |
| Decoding post-rebase focused shape/decoding/protocol gate | PASS, 0.804s; vet PASS |
| Original Linux full ordinary baseline | One failure: TestAnIdleEvaluatorSleeps, 59.32ms extra CPU over two seconds against a 15ms threshold; suite 275.319s |
| Original Linux idle test, isolated repeat | PASS five consecutive runs, 10.131s |
| Final combined Linux ordinary suite | PASS, 279.155s |
| Final combined Linux race suite | PASS, 511.681s |

Final combined source revision: `d149a6a693ca4a1b5d0a29bf25bceb24c6269a9c`. Linux runs use the cached Go 1.27.3 linux/arm64 image, six GiB container memory, a 2048 process ceiling and bounded command timeouts. The initial Docker module-cache mount failed before tests because the host cache was on an inaccessible symlinked volume; a run-owned cache resolved it. The original timing-sensitive failure remains recorded as a failed full baseline, not relabeled green. No test thresholds or production behavior were weakened to accommodate it.

The post-rebase fast gate was scoped to affected decoding/shape/protocol tests plus vet because intervening integration changes were disjoint; the coordinator's full combined Linux ordinary/race gates verify the complete result. No integration commits advanced while those gates used working files.

## Leftovers and mode-gated questions

- **DOC-3 / DOC-I3: stateful custom map-key decoders.** Lazy binding still calls TextUnmarshaler again after JSON decoded each key. A stateful decoder can choose a different key and leave a handle unbound. This pre-existing defect is documented in `issues/stateful-map-key.md` under the run directory, with a reproduction. Stable deterministic key decoders and maps without lazy fields work. A safe fix needs source identity through decoding; the broad decoder and public-method changes were rejected for the concrete compatibility costs above. This remains accepted but unlanded, with no tracker write.
- **Compile-job attribution metadata.** Initial compilation uses the expression keep's lifetime rather than the creation caller's context values. Whether attributed-death callbacks should expose those creation values is not established by the current contract/tests. Normal mode preserves existing behavior; Resolve-job metadata is covered.
- All planned chunks, executors, integrations, reviews and required final checks are complete. The three clean run-owned worktrees and disposable Linux build/module caches were removed. Private executor branches and durable evidence remain recoverable; unrelated worktrees were untouched.

## Top recommendation

Eliminate repeated custom map-key decoding in lazy binding by preserving source identity through the standard JSON decode, while retaining the public Expression method set and encoding/json's single-call side effects. This is the remaining confirmed data-integrity issue from this cycle.
