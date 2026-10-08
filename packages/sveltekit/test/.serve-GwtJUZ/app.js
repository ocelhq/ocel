export default {
  fetch(request) {
    if (new URL(request.url).pathname === "/throws") throw new Error("boom");
    return new Response("ok");
  },
};
