import express from "express";
import { serve } from "inngest/express";
import { inngest } from "./client";
import { onOrderUpdate } from "./functions";

const app = express();

app.use(
  "/api/inngest",
  serve({ client: inngest, functions: [onOrderUpdate] })
);

app.listen(3000, () => {
  console.log("Inngest function server running on http://localhost:3000");
});
