# Reference

These references describe the code in the v2 checkout used to build this site. Kubernetes API pages are generated from `sdk/apis`, the CLI page is generated from `cli/cmd`. No generator fetches API definitions from the 0.x `main` branch.

| Surface | Reference |
| --- | --- |
| Consumer connection, binding, and schema policy | [core.kbind.io](crd/index.md#corekbindiov1alpha1) |
| Provider catalog offerings and collections | [catalog.kbind.io](crd/index.md#catalogkbindiov1alpha1) |
| Provider credential issuance record | [iam.kbind.io](crd/index.md#iamkbindiov1alpha1) |
| CLI command syntax and flags | [CLI](cli/index.md) |
| Optional gateway endpoints and backend options | [Backend](../developers/backend/http/index.md) |
| Consumer agent options | [Konnector](../developers/konnector/controllers/konnector.md) |

The [API concepts guide](../usage/api-concepts.md) explains which objects to create and how they fit together. Generated type descriptions are not a promise that every syntactically valid combination is supported, consult the [synchronization limits](../usage/synchronization.md).
