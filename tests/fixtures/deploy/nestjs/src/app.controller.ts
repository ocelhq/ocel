import { Controller, Get, Res } from "@nestjs/common";
import type { Response } from "express";
import { AppService } from "./app.service";

@Controller()
export class AppController {
  constructor(private readonly service: AppService) {}

  @Get("health")
  health() {
    return { ok: true, app: this.service.app };
  }

  @Get("ocel.svg")
  svg(@Res() res: Response) {
    res.setHeader("content-type", "image/svg+xml");
    res.setHeader("content-length", String(this.service.svg.byteLength));
    res.end(this.service.svg);
  }
}
