import express from "express";

import { mainDb } from "../../../shared/db.js";

export const app = express().get("/", () => mainDb.id);
