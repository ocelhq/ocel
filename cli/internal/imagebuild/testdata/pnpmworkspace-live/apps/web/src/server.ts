import express from "express";
import { greeting } from "@fixture/greeting";

express()
  .get("/", (_request, response) => {
    response.type("text/plain").send(greeting);
  })
  .listen(Number(process.env.PORT ?? 8080));
