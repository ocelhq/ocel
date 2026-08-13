import serverlessExpress from "@codegenie/serverless-express";

import { app } from "./app.js";

export const handler = serverlessExpress({ app });
