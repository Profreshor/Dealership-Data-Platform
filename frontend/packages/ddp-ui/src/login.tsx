import { useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { useAuth } from "./auth";

const credentialsSchema = z.object({ email: z.string().trim().email("Enter a valid email address."), password: z.string().min(8, "Password must be at least 8 characters.") });
type Credentials = z.infer<typeof credentialsSchema>;

export function LoginForm({ onSuccess }: { onSuccess: () => void }) {
  const { login, isLoggingIn } = useAuth();
  const [serverError, setServerError] = useState("");
  const { register, handleSubmit, setError, formState: { errors } } = useForm<Credentials>({ defaultValues: { email: "", password: "" } });
  const submit = handleSubmit(async (values) => {
    setServerError("");
    const parsed = credentialsSchema.safeParse(values);
    if (!parsed.success) {
      for (const issue of parsed.error.issues) setError(issue.path[0] as keyof Credentials, { message: issue.message });
      return;
    }
    try {
      await login(parsed.data);
      onSuccess();
    } catch (error) {
      setServerError(error instanceof Error ? error.message : "Sign in failed. Try again.");
    }
  });

  return <form className="login-form" onSubmit={submit} noValidate>
    <div className="field">
      <label htmlFor="email">Email</label>
      <input id="email" type="email" autoComplete="username" autoFocus aria-invalid={Boolean(errors.email)} aria-describedby={errors.email ? "email-error" : undefined} {...register("email")} />
      {errors.email && <span className="field-error" id="email-error">{errors.email.message}</span>}
    </div>
    <div className="field">
      <label htmlFor="password">Password</label>
      <input id="password" type="password" autoComplete="current-password" aria-invalid={Boolean(errors.password)} aria-describedby={errors.password ? "password-error" : undefined} {...register("password")} />
      {errors.password && <span className="field-error" id="password-error">{errors.password.message}</span>}
    </div>
    {serverError && <div className="form-error" role="alert"><strong>We couldn’t sign you in.</strong><span>{serverError}</span></div>}
    <button className="primary-button" type="submit" disabled={isLoggingIn}>{isLoggingIn ? "Signing in…" : "Sign in"}</button>
  </form>;
}
