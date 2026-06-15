// A self-contained JavaScript action: it only reads inputs and sets an output,
// so it runs anywhere and demonstrates the `tangra` host bridge end-to-end.

const name = tangra.getInput("name");
let message = "hello, " + name + "!";

if (tangra.getInput("shout") === "true") {
  message = message.toUpperCase();
}

tangra.log("built a greeting for " + name);
tangra.setOutput("message", message);
