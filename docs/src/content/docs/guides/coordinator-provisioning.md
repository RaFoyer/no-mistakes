---
title: Coordinator Provisioning
description: Owner-approved provisioning-state manifest for the external shared-runtime admission coordinator on Google Cloud.
---

:::note[Provisioning state]
The owner-approved project resources through workload identity, audit logging,
and budget alerting are provisioned in `agent-organizer-503615`. The immutable
adapter image and serving revision remain unresolved, so Cloud Run remains
undeployed and live admission remains inactive. Other deployment policy
decisions remain pending as recorded below. The command inventory remains
review evidence, not permission to repeat provisioning.
:::

The external coordinator is the authority required by
[shared-runtime admission](/no-mistakes/concepts/daemon/#shared-runtime-admission).
Its production logical identity is `fleet-coordinator-7bef4abe76e2`. The
logical identity is stable; a Google Cloud project, service account, Cloud KMS
key version, or deployment revision is replaceable infrastructure and must
never silently redefine that identity.

## Decision summary

| Decision | Value | State |
| --- | --- | --- |
| Cloud | Google Cloud only | Confirmed constraint |
| Project boundary | Existing project `agent-organizer-503615` | Approved and billing-linked |
| Primary region | `us-east4` | Approved |
| Service runtime | Private authenticated Cloud Run service, scale-to-zero | Pending immutable image |
| Monotonic ledger | Firestore Native mode, `(default)`, `us-east4` | Provisioned |
| Signing | Cloud KMS software asymmetric signing key, `EC_SIGN_ED25519` | Approved for fleet-wide Ed25519 compatibility |
| Workload identity | Attached runtime service account; no downloaded key | Provisioned |
| Client identity | GitHub OIDC restricted to `RaFoyer/no-mistakes` | Provisioned |
| Secret storage | None in v1 unless a non-Google peer credential is proven necessary | Approve omission |
| Production anchor | `fleet-coordinator-7bef4abe76e2` | Confirmed |

The approved project variable is
`FLEET_COORDINATOR_PROJECT_ID=agent-organizer-503615`. Provisioning uses the
isolated coordinator gcloud profile and never infers a project from ambient
configuration.

## Trust and network boundaries

```mermaid
flowchart LR
  client["No-Mistakes adapter"] -->|"authenticated request"| run["Not-deployed private Cloud Run coordinator"]
  run -->|"asymmetricSign only"| kms["Cloud KMS Ed25519 key"]
  run -->|"transactional CAS"| db["Firestore monotonic ledger"]
  run --> logs["Cloud Audit + structured application logs"]
  verifier["No-Mistakes verifier"] -->|"pinned public key + key ID"| claim["Signed claim / lease"]
```

- When deployed, the Cloud Run service must not be public. It must require
  authenticated invocation and reject an identity that is not mapped to an
  allowed runtime and claimant.
- The provisioned runtime service account will be attached directly to Cloud
  Run at deployment. It receives only KMS signing access for the exact key and
  datastore access required by the coordinator.
- No service-account JSON key is created. External clients use short-lived
  credentials through Workload Identity Federation, or another explicitly
  approved Google identity flow.
- The KMS private key is non-exportable. The adapter receives only the public
  key, immutable key-version resource name, key ID, coordinator identity, and
  signed packet.
- Firestore is authoritative for the coordinator generation, per-runtime
  admission state, idempotency keys, and hash-linked transitions. A
  transaction compares the current generation, ledger tip, active-set digest,
  and admission state before appending one transition.
- Repository registries, `NM_HOME`, the daemon database, local receipts, and
  Secret Manager are not authority for signing or monotonic history.
- Cloud Run ingress should be `internal-and-cloud-load-balancing` only if the
  selected client connectivity design includes an authenticated load balancer
  or VPN. Otherwise use authenticated HTTPS with default ingress and IAM
  authorization; never use `--allow-unauthenticated`.
- VPC, load balancer, and static egress resources are omitted from the minimum
  v1 manifest. Add them only with a separately priced, owner-approved network
  design.

## Resource inventory

Provisioned resources carry the labels:
`system=no-mistakes`, `component=admission-coordinator`,
`anchor=fleet-coordinator-7bef4abe76e2`, `environment=prod`, and
`managed-by=governed-provisioning`. Any deployment-pending resource must use
the same labels when it is created.

| Resource | Name | Configuration | State |
| --- | --- | --- | --- |
| GCP project | `agent-organizer-503615` | Existing owner-designated project with billing and budget alerts | Provisioned and billing-linked |
| Cloud Run service | `nm-admission-coordinator-prod` | `us-east4`, request billing, min 0, max 3, concurrency 8, 1 CPU, 512 MiB | Not deployed; immutable image and serving revision pending |
| Runtime service account | `nm-coordinator-runtime` | Attached only to the Cloud Run service | Provisioned; no downloaded key |
| Client principal | `RaFoyer/no-mistakes` GitHub OIDC principal set | `roles/run.invoker` on the one service after deployment | Workload identity provisioned; service binding pending deployment |
| KMS key ring | `nm-admission-prod` | `us-east4` | Provisioned |
| KMS asymmetric key | `fleet-coordinator-signing` | `ASYMMETRIC_SIGN`, `SOFTWARE` protection, `EC_SIGN_ED25519` | Provisioned |
| Initial key version | version `1` | `EC_SIGN_ED25519`, enabled after verification | Provisioned; admission remains inactive |
| Firestore database | `(default)` | Native mode, regional location matching the approved region | Provisioned |
| Artifact Registry | `nm-coordinator` | Docker repository in the approved region | Declared deployment configuration |
| Log bucket | `projects/agent-organizer-503615/locations/us-east4/buckets/nm-admission-audit` | Regional, 365-day retention; no payload bodies or credentials | Provisioned |
| Audit sink | `nm-admission-audit` | Routes KMS Data Access logs to the application-audit bucket | Provisioned |
| Budget | `No-Mistakes admission coordinator monthly` | `$8`; alerts at 50%, 80%, 100% | Provisioned |

Required APIs:

- `run.googleapis.com`
- `cloudkms.googleapis.com`
- `firestore.googleapis.com`
- `iam.googleapis.com`
- `iamcredentials.googleapis.com`
- `sts.googleapis.com` when Workload Identity Federation is selected
- `artifactregistry.googleapis.com`
- `cloudbuild.googleapis.com` only if Google Cloud builds the image
- `logging.googleapis.com`
- `monitoring.googleapis.com`
- `serviceusage.googleapis.com`
- `cloudresourcemanager.googleapis.com`
- `billingbudgets.googleapis.com`

### IAM inventory

| Principal | Scope | Minimum role |
| --- | --- | --- |
| Runtime service account | Exact KMS CryptoKey | `roles/cloudkms.signerVerifier` |
| Runtime service account | Firestore database/project | `roles/datastore.user` |
| Runtime service account | Project | `roles/logging.logWriter`, `roles/monitoring.metricWriter` |
| Approved client principal set | Exact Cloud Run service | `roles/run.invoker` |
| Deployment principal | Exact service and artifact repository | Narrow deployment roles, granted only during governed release |
| Security reviewer group | Project/key/database/log bucket | Read-only metadata, IAM, audit, and public-key access |

Do not grant Owner, Editor, project-wide Service Account User, KMS Admin, or
service-account key creation to the runtime identity. Provisioning and runtime
identities must be separate.

## Signing-key lifecycle

Cloud KMS supports `EC_SIGN_ED25519` for asymmetric signing and accepts raw
message bytes. The coordinator signs one canonical versioned byte encoding; it
does not sign JSON with unstable whitespace or field order.

1. The owner-approved key ring and asymmetric key use `SOFTWARE` protection.
   `EC_SIGN_ED25519` is
   supported with both `SOFTWARE` and `HSM` protection, and the selected
   software configuration preserves the fleet-wide Ed25519 contract. Cloud KMS
   software private-key material remains non-exportable through the service API,
   IAM-gated, and audit-logged.
   See Google's [key purposes and algorithms](https://cloud.google.com/kms/docs/algorithms)
   and [protection levels](https://cloud.google.com/kms/docs/protection-levels)
   references.
2. Retrieve and independently pin the public key, version resource name,
   algorithm, and key ID before enabling admission.
3. Use manual rotation initially. A new version enters `verify-only`, receives
   adversarial interoperability validation, and is then promoted by a signed
   predecessor transition.
4. Keep the prior version enabled for verification during a 30-day overlap.
   Disable it only after all maximum packet lifetimes and rollback windows pass.
5. Schedule destruction no sooner than 90 days after disablement, subject to
   audit and incident-retention policy.
6. A rollback changes the serving revision or active signing version through a
   governed transition. It never rewrites the ledger or reuses a coordinator
   generation.

## Ledger, audit, and retention

Firestore is the selected ledger because its transactions can atomically compare
and update the per-runtime admission document while appending an immutable
transition document. The implementation must also reject:

- stale or future generation numbers;
- a predecessor hash different from the current ledger tip;
- reused request, claim, or transition idempotency keys;
- changed active sets between preparation and start;
- delayed starts outside the comparison window;
- terminal transitions from any claimant other than the lease owner;
- attempts to reopen admission without the corresponding terminal append.

The ledger is append-only at the application and IAM layers. Scheduled exports
or backups are disaster-recovery evidence, not a second writable authority.
Enable Firestore point-in-time recovery only after its incremental cost and
restore procedure are approved.

Cloud Audit Logs must cover KMS, IAM, Cloud Run administration, and Firestore
data access. `_Required` retains its covered logs for 400 days. The provisioned
custom application-audit bucket retains sanitized transition metadata for 365
days. Logs contain resource IDs, bounded hashes, generation, transition,
latency, result, and caller identity—not claim payloads, repository paths,
tokens, credentials, prompts, or source content.

The project IAM audit configuration enables `DATA_READ` for
`cloudkms.googleapis.com`. The provisioned
`projects/agent-organizer-503615/locations/us-east4/buckets/nm-admission-audit`
bucket has 365-day retention. Its `nm-admission-audit` sink targets that bucket
and filters for log names containing
`cloudaudit.googleapis.com%2Fdata_access` and
`protoPayload.serviceName=cloudkms.googleapis.com`. Structural validation has
confirmed the audit policy, bucket, and sink; an end-to-end signing audit-log
event has not yet been observed.

Alert on:

- rejected signatures, predecessor conflicts, replay, or generation rollback;
- admission closed beyond the maximum lease plus reconciliation budget;
- KMS or Firestore error-rate and latency thresholds;
- unexpected service revision, IAM change, key state change, or key use;
- billing thresholds.

## Secret Manager decision

Secret Manager is **not required** by the minimum design. The KMS private key
never leaves KMS, the runtime identity is provisioned for later Cloud Run
attachment, and external clients should use short-lived federated credentials.

If a later transport requires a non-Google private credential, add one
repository-scoped Secret Manager secret, grant the runtime service account
access to that exact secret version only, disable environment-variable
injection, and document rotation and rollback. That is a new HITL provisioning
decision; it is not authorized by this manifest.

## Cost estimate

This estimate is a planning input in USD, using public list prices as of the
document revision date. Actual price depends on the selected region, billing
account, free-tier use shared by that account, log volume, and traffic.

Assumptions: one scale-to-zero service, 100,000 coordinator requests/month,
500 ms average request duration, 1 vCPU/512 MiB, 300,000 Firestore reads,
300,000 writes, 100,000 KMS signatures, less than 1 GiB database storage, less
than 5 GiB logs, and negligible same-region transfer.

| Item | Estimated monthly cost |
| --- | ---: |
| Cloud Run | `$0–$2` |
| Firestore Standard | `$0–$1` |
| Cloud KMS software Ed25519 key: one active version + 100k signs | about `$0.36` |
| Artifact Registry | `$0–$1` |
| Cloud Logging/Monitoring | `$0` under included ingestion; retention/volume can add cost |
| **Expected software-key total** | **`$1–$5/month`** |

The provisioned budget alerts at 50%, 80%, and 100% are not a hard spending
cap. Recalculate with the
[Google Cloud Pricing Calculator](https://cloud.google.com/products/calculator)
before approving any material configuration or traffic change. Pricing sources:
[Cloud KMS](https://cloud.google.com/kms/pricing),
[Cloud Run](https://cloud.google.com/run/pricing),
[Firestore](https://cloud.google.com/firestore/pricing), and
[Cloud Logging retention](https://cloud.google.com/logging/quotas#logs_retention_periods).

## Provisioning command inventory

The following records approved values and command shape. Commands for
provisioned resources are historical inventory only and must not be repeated.
The Artifact Registry and Cloud Run commands are deployment-pending and must
not run until separately authorized; Cloud Run also requires an immutable adapter
image and serving revision.

```sh
FLEET_COORDINATOR_PROJECT_ID='agent-organizer-503615'
FLEET_COORDINATOR_REGION='us-east4'
FLEET_COORDINATOR_BILLING_ACCOUNT='01BEFD-7B2FB4-93B084'
FLEET_COORDINATOR_CLIENT_MEMBER='principalSet://iam.googleapis.com/projects/67974302072/locations/global/workloadIdentityPools/no-mistakes-github/attribute.repository/RaFoyer/no-mistakes'
FLEET_COORDINATOR_IMAGE='<REQUIRED_IMMUTABLE_ARTIFACT_DIGEST>'
FLEET_COORDINATOR_BUDGET_USD='8'

# Historical inventory — completed; do not repeat.
gcloud billing projects link "${FLEET_COORDINATOR_PROJECT_ID}" \
  --billing-account="${FLEET_COORDINATOR_BILLING_ACCOUNT}"

gcloud services enable \
  run.googleapis.com cloudkms.googleapis.com firestore.googleapis.com \
  iam.googleapis.com iamcredentials.googleapis.com sts.googleapis.com \
  artifactregistry.googleapis.com logging.googleapis.com \
  monitoring.googleapis.com serviceusage.googleapis.com \
  cloudresourcemanager.googleapis.com billingbudgets.googleapis.com \
  --project="${FLEET_COORDINATOR_PROJECT_ID}"

gcloud iam service-accounts create nm-coordinator-runtime \
  --display-name='No-Mistakes admission coordinator runtime' \
  --project="${FLEET_COORDINATOR_PROJECT_ID}"

gcloud kms keyrings create nm-admission-prod \
  --location="${FLEET_COORDINATOR_REGION}" \
  --project="${FLEET_COORDINATOR_PROJECT_ID}"
gcloud kms keys create fleet-coordinator-signing \
  --keyring=nm-admission-prod \
  --location="${FLEET_COORDINATOR_REGION}" \
  --purpose=asymmetric-signing \
  --default-algorithm=ec-sign-ed25519 \
  --protection-level=software \
  --project="${FLEET_COORDINATOR_PROJECT_ID}"

gcloud firestore databases create \
  --database='(default)' \
  --location="${FLEET_COORDINATOR_REGION}" \
  --type=firestore-native \
  --project="${FLEET_COORDINATOR_PROJECT_ID}"

# Deployment-pending — do not execute until separately authorized.
gcloud artifacts repositories create nm-coordinator \
  --repository-format=docker \
  --location="${FLEET_COORDINATOR_REGION}" \
  --project="${FLEET_COORDINATOR_PROJECT_ID}"

# Deployment-pending — do not execute until separately authorized.
gcloud run deploy nm-admission-coordinator-prod \
  --image="${FLEET_COORDINATOR_IMAGE}" \
  --region="${FLEET_COORDINATOR_REGION}" \
  --service-account="nm-coordinator-runtime@${FLEET_COORDINATOR_PROJECT_ID}.iam.gserviceaccount.com" \
  --no-allow-unauthenticated \
  --min=0 --max=3 --concurrency=8 --cpu=1 --memory=512Mi \
  --project="${FLEET_COORDINATOR_PROJECT_ID}"

# Deployment-pending — do not execute until separately authorized.
gcloud run services add-iam-policy-binding nm-admission-coordinator-prod \
  --region="${FLEET_COORDINATOR_REGION}" \
  --member="${FLEET_COORDINATOR_CLIENT_MEMBER}" \
  --role=roles/run.invoker \
  --project="${FLEET_COORDINATOR_PROJECT_ID}"
```

The final reviewed deployment manifest must record as-built resource-level IAM
commands and the Workload Identity Pool/provider mapping, then add metric
alerts, Firestore indexes/rules, service configuration, the immutable adapter
image digest, and the serving revision. These remaining values depend on owner
selections and implemented API fields and must not be guessed here.

## Installation, validation, and rollback

### Stage 0: source-only

- Implement coordinator service, datastore schema, canonical encoding, client
  adapter, and adversarial fixtures without cloud credentials.
- Use emulators or deterministic fakes for local tests.
- Hold pull requests for coordinator review.

### Stage 1: governed provisioning

The owner approved the recorded project, billing account, region, KMS
protection, WIF repository scope, IAM boundary, database, retention, budget,
alerts, and cost estimate before the completed provisioning transaction. Any
new resource or material configuration change requires a separately authorized
transaction.

### Stage 2: isolated staging

- Provision staging equivalents in the same repository-scoped project or a
  separately approved staging project.
- Deploy an immutable image digest with no live No-Mistakes endpoint configured.
- Verify IAM denial cases, KMS public-key identity, ledger transactions,
  idempotent ambiguous-response reconciliation, audit coverage, alerts, and
  backup/restore.
- Run forged, replayed, stale, future, delayed, changed-active-set, key-rotation,
  datastore-outage, KMS-outage, and network-partition tests.

### Stage 3: installation validation

- Package the No-Mistakes adapter with a pinned coordinator identity, endpoint,
  public key/version, accepted algorithms, runtime scope, and time bounds.
- Independently verify every raw start path consults the adapter before any
  cancellation or local run/worktree write.
- Install through governed distribution with exact binary, config, public-key,
  and rollback hashes.
- Do not restart or mutate the live daemon or active runs during validation.

### Stage 4: governed activation

- Snapshot the exact active set and verify no unmanaged start path exists.
- Activate only the approved runtime scopes through a new owner decision.
- Start with ordinary run admission. Destructive recovery remains disabled
  until action-bound recovery claims and their independent tests are merged,
  installed, and approved.

### Rollback

- Close new admission at the coordinator before changing a serving revision.
- Preserve the ledger and coordinator generation; never restore an older
  database snapshot over the current authority.
- Roll Cloud Run back to a previously verified immutable image.
- Keep the current KMS version available for verification. Change the signing
  version only through a monotonic rotation transition.
- Restore the prior No-Mistakes binary/config from recorded hashes without
  synthesizing local leases or reopening admission.
- If coordinator state cannot be proven, fresh starts remain fail-closed while
  existing runs and the daemon remain untouched.

## Remaining deployment decisions

Project placement, billing, region, signing protection, WIF repository scope,
and the `$8` budget are approved and provisioned. Deployment remains blocked
until the owner reviews:

1. Firestore PITR/backup policy and recovery objectives.
2. 365-day application-audit retention and any export destination.
3. Whether staging shares the production project or receives a distinct
   repository-scoped project.
4. Final ingress design after client location is known.
5. The immutable container digest and exact serving revision.
6. Exact deployment manifest revision after implementation fixes the API
   fields and datastore indexes.

Until those decisions and a separate deployment authorization exist, Cloud Run
remains absent and live admission stays inactive.
