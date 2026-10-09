import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useQueryClient } from "@tanstack/react-query";
import { z } from "zod";
import { ApiResponseError, createApiClient } from "./api";
import { clearAuthSession } from "./auth";

const emailSchema = z.object({ email: z.string().trim().email("Enter a valid email address.") });
const passwordSchema = z.object({
  password: z.string().refine((value) => new TextEncoder().encode(value).length >= 8, "Password must be at least 8 bytes.").refine((value) => new TextEncoder().encode(value).length <= 128, "Password must be 128 UTF-8 bytes or fewer."),
  confirmation: z.string(),
}).superRefine((values, context) => {
  if (values.password !== values.confirmation) context.addIssue({ code: z.ZodIssueCode.custom, path: ["confirmation"], message: "Passwords must match." });
});

function apiMessage(error: unknown, fallback: string) {
  if (!(error instanceof ApiResponseError)) return fallback;
  if (error.status === 429 || error.code === "ratelimit" || error.code === "rate_limited") return "Too many attempts. Please wait a moment and try again.";
  if (error.code === "invalidlink" || error.code === "invalid_link") return "This password link is invalid or has expired.";
  if (error.code === "invalidpassword" || error.code === "invalid_password") return "Choose a password that meets the requirements.";
  return fallback;
}

export function ResetRequestForm() {
  const [status, setStatus] = useState<"" | "success" | "error">("");
  const [serverError, setServerError] = useState("");
  const { register, handleSubmit, setError, formState: { errors, isSubmitting } } = useForm<{ email: string }>({ defaultValues: { email: "" } });
  const submit = handleSubmit(async (values) => {
    setStatus("");
    setServerError("");
    const parsed = emailSchema.safeParse(values);
    if (!parsed.success) {
      for (const issue of parsed.error.issues) setError("email", { message: issue.message });
      return;
    }
    try {
      await createApiClient().request("/auth/reset-request", z.object({ accepted: z.literal(true) }), { method: "POST", body: JSON.stringify(parsed.data) });
      setStatus("success");
    } catch (error) {
      setServerError(apiMessage(error, "We couldn’t send a reset link. Try again."));
      setStatus("error");
    }
  });

  return <form className="login-form" onSubmit={submit} noValidate>
    <div className="field">
      <label htmlFor="reset-email">Email</label>
      <input id="reset-email" type="email" autoComplete="email" autoFocus aria-invalid={Boolean(errors.email)} aria-describedby={errors.email ? "reset-email-error" : undefined} {...register("email")} />
      {errors.email && <span className="field-error" id="reset-email-error">{errors.email.message}</span>}
    </div>
    {status === "success" && <div className="form-success" role="status">If an account uses that email, a reset link is on its way.</div>}
    {status === "error" && <div className="form-error" role="alert">{serverError}</div>}
    <button className="primary-button" type="submit" disabled={isSubmitting}>{isSubmitting ? "Sending…" : "Send reset link"}</button>
  </form>;
}

function readToken() {
  return new URLSearchParams(window.location.hash.slice(1)).get("token") ?? "";
}

export function PasswordSetupForm() {
  const queryClient = useQueryClient();
  const [token] = useState(readToken);
  const [status, setStatus] = useState<"" | "success" | "error">("");
  const [serverError, setServerError] = useState("");
  const { register, handleSubmit, setError, formState: { errors, isSubmitting } } = useForm<{ password: string; confirmation: string }>({ defaultValues: { password: "", confirmation: "" } });

  useEffect(() => {
    if (window.location.hash) window.history.replaceState(null, "", window.location.pathname);
  }, []);

  if (!token) return <div className="form-error" role="alert"><strong>Invalid password link.</strong><span>This link is missing or expired. Request a new one to continue.</span></div>;

  const submit = handleSubmit(async (values) => {
    setStatus("");
    setServerError("");
    const parsed = passwordSchema.safeParse(values);
    if (!parsed.success) {
      for (const issue of parsed.error.issues) setError(issue.path[0] as "password" | "confirmation", { message: issue.message });
      return;
    }
    try {
      await createApiClient().request("/auth/password", z.object({ changed: z.literal(true) }), { method: "POST", body: JSON.stringify({ token, password: parsed.data.password }) });
      clearAuthSession(queryClient);
      setStatus("success");
    } catch (error) {
      setServerError(apiMessage(error, "We couldn’t update your password. Try again."));
      setStatus("error");
    }
  });

  if (status === "success") return <div className="form-success" role="status"><strong>Password updated.</strong><span>Your password is ready. <a href="/login">Sign in normally</a>.</span></div>;
  return <form className="login-form" onSubmit={submit} noValidate>
    <div className="field">
      <label htmlFor="new-password">New password</label>
      <input id="new-password" type="password" autoComplete="new-password" autoFocus aria-invalid={Boolean(errors.password)} aria-describedby={errors.password ? "new-password-error" : undefined} {...register("password")} />
      {errors.password && <span className="field-error" id="new-password-error">{errors.password.message}</span>}
    </div>
    <div className="field">
      <label htmlFor="password-confirmation">Confirm new password</label>
      <input id="password-confirmation" type="password" autoComplete="new-password" aria-invalid={Boolean(errors.confirmation)} aria-describedby={errors.confirmation ? "password-confirmation-error" : undefined} {...register("confirmation")} />
      {errors.confirmation && <span className="field-error" id="password-confirmation-error">{errors.confirmation.message}</span>}
    </div>
    {status === "error" && <div className="form-error" role="alert">{serverError}</div>}
    <button className="primary-button" type="submit" disabled={isSubmitting}>{isSubmitting ? "Updating…" : "Set password"}</button>
  </form>;
}
