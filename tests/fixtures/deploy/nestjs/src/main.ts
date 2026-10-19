import "reflect-metadata";
import { NestFactory } from "@nestjs/core";
import { AppModule } from "./app.module";
import { probes } from "./probes";

async function bootstrap() {
  const app = await NestFactory.create(AppModule, { bodyParser: false });
  app.use("/api/probes", probes);
  const port = Number(process.env.PORT ?? 3108);
  await app.listen(port);
  console.log(`nest listening on http://localhost:${port}`);
}
bootstrap();
