# Glossary

The words every part of Ocel shares, one meaning each. An entry is a concept that crosses
vendors, frameworks or packages and has been, or could be, confused; a vendor's internal
names never belong here. Agents never edit this file: a term that seems to need an entry
goes under **Found, not fixed** in the PR, and a maintainer decides.

| Term | Means | Never |
| --- | --- | --- |
| build | what `ocel build` produces for one app, named by its build ID | a deploy, a release |
| release | one app's build bound to a promotion, environment and values | a whole-project deploy |
| release token | the short token that names a release in physical names | a release |
| storage prefix | where a release's objects live: `<env>/<project>/<app>/<release token>/` | |
| promotion | the releases, one per app, that one deploy makes live | |
| deployment | a whole-project deploy: one promotion as users see it, what `ocel deployments` lists | anything per-app |
| plan | the diff a human consents to | desired state |
| spec | the desired state a provider is handed | a consented diff |
| vendor state | what a vendor keeps for itself through a run | bare `state` |
| origin | the cloud an app runs in | |
| edge | what serves and routes requests in front of an origin | |
| console | Ocel's optional hosted control plane | a cloud |
