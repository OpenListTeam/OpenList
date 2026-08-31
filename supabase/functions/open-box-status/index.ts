import "jsr:@supabase/functions-js/edge-runtime.d.ts";

const allowedOrigin = "https://open-box.space";

Deno.serve(async (req: Request) => {
  const headers = {
    "content-type": "application/json; charset=utf-8",
    "cache-control": "no-store",
    "access-control-allow-origin": allowedOrigin,
    "access-control-allow-headers": "authorization, apikey, content-type",
    "access-control-allow-methods": "GET, OPTIONS",
    vary: "Origin",
  };
  if (req.method === "OPTIONS") return new Response(null, { status: 204, headers });
  if (req.method !== "GET") {
    return new Response(JSON.stringify({ error: "method_not_allowed" }), { status: 405, headers });
  }
  return new Response(JSON.stringify({
    service: "open-box",
    status: "ok",
    auth: "required",
    timestamp: new Date().toISOString(),
  }), { status: 200, headers });
});
