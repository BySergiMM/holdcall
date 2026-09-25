"use client";

import { useState } from "react";

type State = "idle" | "sending" | "done" | "closed" | "invalid" | "failed";

// The one interactive thing on the product page. It talks to /api/waitlist,
// a function that stores nothing but the address and the time; when that
// function answers 503 the store behind it does not exist yet, and the form
// says so instead of pretending.
export default function Waitlist() {
  const [email, setEmail] = useState("");
  const [state, setState] = useState<State>("idle");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(email)) {
      setState("invalid");
      return;
    }
    setState("sending");
    try {
      const r = await fetch("/api/waitlist", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ email }),
      });
      if (r.status === 503) setState("closed");
      else if (r.ok) setState("done");
      else setState("failed");
    } catch {
      setState("failed");
    }
  }

  if (state === "done") {
    return (
      <p className="lp-form-note" role="status">
        You are on the list. We write once, when the team console opens, and never for anything else.
      </p>
    );
  }
  if (state === "closed") {
    return (
      <p className="lp-form-note" role="status">
        Sign-ups open in October. Until then the <a href="/status/">status page</a> shows everything that ships.
      </p>
    );
  }

  return (
    <form className="lp-form" onSubmit={submit} noValidate>
      <label className="lp-visually-hidden" htmlFor="lp-email">
        Work email
      </label>
      <input
        id="lp-email"
        type="email"
        name="email"
        inputMode="email"
        autoComplete="email"
        placeholder="you@company.com"
        value={email}
        onChange={(e) => {
          setEmail(e.target.value);
          if (state === "invalid") setState("idle");
        }}
        aria-invalid={state === "invalid"}
        aria-describedby="lp-form-help"
      />
      <button type="submit" disabled={state === "sending"}>
        {state === "sending" ? "Adding you…" : "Get early access"}
      </button>
      <p id="lp-form-help" className="lp-form-help" aria-live="polite">
        {state === "invalid" && "That does not look like an email address."}
        {state === "failed" && "Something went wrong on our side. Try again in a minute."}
        {(state === "idle" || state === "sending") &&
          "One address, stored until the team console opens, deleted on request."}
      </p>
    </form>
  );
}
