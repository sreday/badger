# One-off: lets GitHub deploy Snout to scan.sreday.com (.github/workflows/deploy.yml).
# Run once, signed in to gcloud as the project's owner. It stores no key anywhere:
# GitHub proves who it is per run (workload identity), and Google accepts only
# this repo's main branch. The identity can do one thing: manage the one server.
# Undo: delete the service account github-deploy and the identity pool "github".
$p = 'sreday-badger-scan'
$n = gcloud projects describe $p --format="value(projectNumber)"
$sa = "github-deploy@$p.iam.gserviceaccount.com"
gcloud services enable iam.googleapis.com iamcredentials.googleapis.com sts.googleapis.com --project $p
gcloud iam service-accounts create github-deploy --project $p --display-name "GitHub deploys of Snout (sreday/badger main)"
gcloud iam workload-identity-pools create github --project $p --location global --display-name "GitHub Actions"
gcloud iam workload-identity-pools providers create-oidc badger --project $p --location global --workload-identity-pool github --issuer-uri "https://token.actions.githubusercontent.com" --attribute-mapping "google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.ref=assertion.ref" --attribute-condition "assertion.repository=='sreday/badger' && assertion.ref=='refs/heads/main'"
gcloud iam service-accounts add-iam-policy-binding $sa --project $p --role roles/iam.workloadIdentityUser --member "principalSet://iam.googleapis.com/projects/$n/locations/global/workloadIdentityPools/github/attribute.repository/sreday/badger" --format="value(etag)"
gcloud compute instances add-iam-policy-binding badger-scan --zone us-central1-a --project $p --member "serviceAccount:$sa" --role roles/compute.instanceAdmin.v1 --format="value(etag)"
gcloud iam service-accounts add-iam-policy-binding "$n-compute@developer.gserviceaccount.com" --project $p --member "serviceAccount:$sa" --role roles/iam.serviceAccountUser --format="value(etag)"
gcloud projects add-iam-policy-binding $p --member "serviceAccount:$sa" --role roles/compute.viewer --condition None --format="value(etag)"
"Done. Provider:"
gcloud iam workload-identity-pools providers describe badger --project $p --location global --workload-identity-pool github --format="value(name,attributeCondition,state)"
