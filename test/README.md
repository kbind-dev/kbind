# Tests

This is the test catalog for the kbind slim core: every scenario, what it
asserts, and what is **not** covered yet. Keep it in sync when adding tests.

## How the tests run

- **e2e** (`test/e2e/`) run on **envtest** — real `kube-apiserver` + `etcd`
  binaries as local processes (no kind, no Docker, no kubelet/nodes). Each test
  starts **two** control planes: a *consumer* (pre-loaded with the core CRDs) and
  a *provider*, and runs the real engine reconcilers in-process against the
  consumer. See [e2e/framework/framework.go](e2e/framework/framework.go).
- **unit** tests live next to the code under `engine/*/` and need no cluster.

```sh
make test        # unit tests only (no external setup)
make test-e2e    # downloads envtest assets and runs the e2e suite

# one e2e test, verbose:
KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.21 use 1.34.1 -p path)" \
  go test -v ./test/e2e -run TestSlimCoreHappyCase
```

## Direction conventions

- **Bound instances** (the synced CRs): **spec** flows consumer→provider, **status**
  flows provider→consumer. There is no other direction by design.
- **Related resources** (Secrets/ConfigMaps): direction is per-rule —
  `FromProvider` (provider→consumer) or `FromConsumer` (consumer→provider).

---

## E2E scenarios (`test/e2e/`)

### `TestSlimCoreHappyCase` — [sync_test.go](e2e/sync_test.go)
The full one-apply flow (Secret + Connection + ClusterBinding), stepped in order:

| # | Scenario | Asserts |
|---|---|---|
| 1 | Connection becomes Ready and discovers the exported API | discovery → `status.exportedAPIs`, Ready |
| 2 | provider heartbeat Lease is maintained and renewed | `coordination.k8s.io/Lease` on provider, renewed |
| 3 | ClusterBinding Ready and the CRD is pulled onto the consumer | `schema.source: CRD` pull, `boundAPIs`, managed marker |
| 4 | instance spec syncs consumer → provider | SSA + ownership markers + syncer finalizer |
| 5 | status syncs provider → consumer | status subresource flows down |
| 6 | spec update syncs consumer → provider | update propagation |
| 7 | conflict: foreign provider object not overwritten | `conflictPolicy: Fail`, `conflictCount` + `Conflicts` condition |
| 8 | consumer delete removes the provider copy + releases finalizer | delete propagation |
| 9 | deleting a conflicting consumer object leaves the foreign provider object intact | conflict-safe delete |
| 10 | a CRD exported after connect is picked up | periodic re-discovery |
| 11 | `conflictPolicy: Adopt` takes over an un-owned provider object | adopt (never steals an owned object) |
| 12 | Connection created before its Secret resolves when the Secret arrives | order-independence (Secret watch) |
| 13 | Secret survives deletion while its Connection exists, released on Connection delete | Secret finalizer |
| 14 | a Secret shared by multiple Connections released only when the last is deleted | shared-Secret refcount |
| 15 | deleting the ClusterBinding unbinds and cleans up | full unbind |

### `TestSlimCorePolicies` — [policies_test.go](e2e/policies_test.go)
| Scenario | Asserts |
|---|---|
| `deletion-policy: Orphan` keeps the provider copy | orphan on delete/unbind |
| `updatePolicy: Always` follows provider CRD changes | schema tracking |
| `autoBind` maintains a managed ClusterBinding | auto-bind mirrors exported APIs |
| `pullPolicy: All` installs CRDs without a binding | pull-all |
| `PermissionDenied` surfaces on a restricted Connection | provider RBAC denial → condition/Event |

### `TestSlimCoreRelatedResources` — [related_resources_test.go](e2e/related_resources_test.go)
| Scenario | Asserts |
|---|---|
| a label-selected provider **Secret** syncs to the consumer | `FromProvider` + labelSelector |
| the synced copy is GC'd when it stops matching | GC on stop-matching |

### `TestSlimCoreRelatedConfigMaps` — [related_resources_more_test.go](e2e/related_resources_more_test.go)
| Scenario | Asserts |
|---|---|
| a label-selected provider **ConfigMap** syncs to the consumer | `FromProvider` ConfigMap |
| the synced ConfigMap is GC'd when it stops matching | GC |
| a foreign ConfigMap of the same name is not overwritten | foreign-object guard |

### `TestSlimCoreRelatedReverseDirection` — [related_resources_more_test.go](e2e/related_resources_more_test.go)
| Scenario | Asserts |
|---|---|
| a label-selected consumer **Secret** syncs **up** to the provider | `FromConsumer` (reverse) |
| the provider copy is GC'd when the consumer Secret stops matching | reverse GC |

### `TestSlimCoreRelatedNamedSelector` — [related_resources_more_test.go](e2e/related_resources_more_test.go)
| Scenario | Asserts |
|---|---|
| only the named ConfigMap syncs; a non-named one does not | `selector.names` |

### `TestSlimCoreRelatedCleanupOnUnbind` — [related_resources_more_test.go](e2e/related_resources_more_test.go)
| Scenario | Asserts |
|---|---|
| deleting the binding removes the related copies | `cleanupRelated` on unbind |

### `TestSlimCoreOpenAPISource` — [schema_source_test.go](e2e/schema_source_test.go)
| Scenario | Asserts |
|---|---|
| Connection synthesizes and installs the CRD via OpenAPI | `schema.source: OpenAPI` (CRD-less provider) |
| a binding syncs an instance over the synthesized CRD | sync works on a synthesized CRD |

### `TestSlimCoreKCPLikeProvider` — [schema_source_test.go](e2e/schema_source_test.go)
| Scenario | Asserts |
|---|---|
| identity is pinned from the LogicalCluster | kcp identity (LogicalCluster UID over kube-system) |
| instances sync against the kcp-like provider | end-to-end on a kcp-shaped provider |

### `TestSlimCoreStopOnDisengage` — [disengage_test.go](e2e/disengage_test.go)
| Scenario | Asserts |
|---|---|
| a Connection that loses readiness disengages | syncers torn down, not left against a dead cluster |
| re-engage rebuilds the syncer and sync resumes | fresh cluster on re-engage |

### `TestSlimCoreNamespacedBinding` — [namespaced_binding_test.go](e2e/namespaced_binding_test.go)
| Scenario | Asserts |
|---|---|
| the namespaced Binding becomes Ready and pulls the CRD | namespaced `Binding` kind reconciles |
| an instance in the bound namespace syncs to the provider | namespace-scoped sync |
| an instance in another namespace is not synced | scope enforcement (ClusterBinding would cover all; a Binding only its namespace) |

---

## Unit tests (`engine/*/`)

### `engine/crdpull` — [crdpull_test.go](../engine/crdpull/crdpull_test.go)
| Test | Covers |
|---|---|
| `TestPull_BoundCreatesCRD` | default pull creates the CRD |
| `TestPull_UpdatePolicyOnce_DoesNotUpdate` | **`updatePolicy: Once`** pins the schema |
| `TestPull_UpdatePolicyAlways_FollowsProviderChanges` | `updatePolicy: Always` |
| `TestPull_NoneAbsent_NotInstalled` | **`pullPolicy: None`** does not install |
| `TestPull_NonePresent_StampsMarkers` | `pullPolicy: None` still stamps markers on an existing CRD |

### `engine/remote` — [remote_test.go](../engine/remote/remote_test.go)
| Test | Covers |
|---|---|
| `TestClusterUID_KubeSystem` | identity from kube-system on plain k8s |
| `TestClusterUID_KCPLogicalClusterFallback` | identity from LogicalCluster |
| `TestClusterUID_LogicalClusterWinsWhenBothPresent` | LogicalCluster preferred |
| `TestClusterUID_NoSource` | error when neither is present |

### `engine/mapper` — [mapper_test.go](../engine/mapper/mapper_test.go)
| Test | Covers |
|---|---|
| `TestIdentity_RoundTrips` | `Identity` mapper key round-trip |
| `TestMapper_NonIdentityRoundTrips` | a **custom (non-identity) Mapper** round-trip contract |

---

## Coverage matrix (by feature)

| Feature | e2e | unit |
|---|---|---|
| Connection: secret resolve, identity pin, discovery | ✅ | — |
| Heartbeat Lease | ✅ | — |
| Schema delivery: CRD pull | ✅ | ✅ |
| Schema delivery: OpenAPI synthesis / Auto | ✅ | — |
| `pullPolicy`: Bound (default), All | ✅ | — |
| `pullPolicy`: None | — | ✅ |
| `updatePolicy`: Always | ✅ | ✅ |
| `updatePolicy`: Once | — | ✅ |
| Instance sync: spec up / status down / update | ✅ | — |
| Conflicts: `Fail` (no overwrite), `Adopt` | ✅ | — |
| `deletion-policy: Orphan` | ✅ | — |
| `autoBind` | ✅ | — |
| Order-independent apply + Secret lifecycle | ✅ | — |
| Unbind / cleanup (instances, CRD, related) | ✅ | — |
| Related: `FromProvider` Secret + ConfigMap, GC, foreign guard | ✅ | — |
| Related: `FromConsumer` (reverse) Secret, GC | ✅ | — |
| Related: `names` selector | ✅ | — |
| Stop-on-disengage + re-engage | ✅ | — |
| kcp `LogicalCluster` identity | ✅ | ✅ |
| `PermissionDenied` / provider RBAC | ✅ | — |
| Mapper extension point | Identity only (implicit) | ✅ (contract) |

## Not covered yet

- **Custom `Mapper` end-to-end** — the contract is unit-tested; no e2e wires a
  non-identity mapper through the running syncer.
- **`pullPolicy: None` / `updatePolicy: Once` in e2e** — unit-tested in `crdpull`.
- **`FromConsumer` ConfigMap** — only the `FromConsumer` Secret path is e2e'd
  (direction is resource-kind-agnostic in the code, so this is cosmetic).
- **Conversion-webhook CRDs** — not refused yet; not tested (known gap).
- **Multi-version CRDs** — accepted limitation; not tested.
