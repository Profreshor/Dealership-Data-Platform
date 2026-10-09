import { registerPage } from "@ddp/ui";

function BrokenReport() {
  if (sessionStorage.getItem("ddp_retry_ready") !== "1") {
    throw new Error("Synthetic component failure");
  }
  return <h1>Recovered client page</h1>;
}

registerPage("broken_report", BrokenReport);
