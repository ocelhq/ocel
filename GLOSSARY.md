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
| runtime | the layer between an app's code and the target it runs on, which turns Ocel's contracts into that target's implementation; also the code shipped alongside it | the language or framework an app is written in |
| framework | what a build targets: a language (`go`, `python`, `rust`, `node`) or a JS framework (`next`); one field, never split into language and runtime, since every JS framework implies node | a runtime |
| hosting | what a build states for whoever serves it, in `hosting.json`: its version, framework, root function, route table, static rules and needs | a serve descriptor |
| root function | the function a build serves its root route from, which every request no route table sends elsewhere reaches; named by its route ID | an entry, an entry function |
| route table | a framework's own rules for which function or file serves each path, in a file named for its format (`next-route-table.json`) and read by that framework's router at the edge or the origin | a routing manifest |
| immutable prefix | a path prefix a build's static rules name: a file under it is cached forever unless a must-revalidate prefix also covers it, and a miss under it is a 404 rather than a request to a function | a framework's static path |
