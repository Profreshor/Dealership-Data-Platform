import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { z } from "zod";
import { ApiResponseError, createApiClient } from "./api";
import { authSessionKey, useAuth } from "./auth";

const directorySchema = z.object({ users: z.array(z.object({ id: z.string(), email: z.string(), admin: z.boolean(), disabled: z.boolean(), pending: z.boolean(), roles: z.array(z.string()) })), roles: z.array(z.object({ id: z.string(), name: z.string(), permissions: z.array(z.string()) })), permissions: z.array(z.string()) });
const changedSchema = z.object({ changed: z.literal(true) });
const inviteSchema = z.object({ id: z.string(), email: z.string(), roles: z.array(z.string()), status: z.string() });
const acceptedSchema = z.object({ accepted: z.literal(true) });
type Directory = z.infer<typeof directorySchema>;
type EditableRole = Directory["roles"][number] & { isNew: boolean };

function errorText(error: unknown) { return error instanceof Error ? error.message : "Something went wrong."; }
function ErrorState({ error, retry }: { error: unknown; retry: () => void }) { return <div className="page-error" role="alert"><strong>Couldn’t load administration.</strong><span>{errorText(error)}</span><button type="button" onClick={retry}>Try again</button></div>; }

export function ProfilePage() {
  const { session } = useAuth();
  const queryClient = useQueryClient();
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const reset = useMutation({ mutationFn: () => createApiClient({ csrfToken: session?.csrf_token }).request("/auth/reset-request", acceptedSchema, { method: "POST", body: JSON.stringify({ email: session?.user.email }) }), onSuccess: () => { setError(""); setMessage("If your account can receive email, a password reset link is on its way."); }, onError: (e) => { if (e instanceof ApiResponseError && e.status === 401) void queryClient.invalidateQueries({ queryKey: authSessionKey }); setMessage(""); setError(errorText(e)); } });
  if (!session) return null;
  return <section className="admin-page" aria-labelledby="profile-title"><div className="page-heading"><div><h1 id="profile-title">Your profile</h1><p>Review your access and account details.</p></div></div>
    <div className="admin-grid"><article className="admin-card"><h2>Account</h2><dl className="detail-list"><div><dt>Email</dt><dd>{session.user.email}</dd></div><div><dt>Account type</dt><dd>{session.user.admin ? "Operator" : "Member"}</dd></div></dl><button className="primary-button" type="button" disabled={reset.isPending} onClick={() => reset.mutate()}>{reset.isPending ? "Sending…" : "Send password reset link"}</button>{message && <p className="form-success" role="status">{message}</p>}{error && <p className="field-error" role="alert">{error}</p>}</article>
      <article className="admin-card"><h2>Effective permissions</h2>{session.user.admin ? <p className="muted">Operator access bypasses client permission grants.</p> : session.user.permissions.length ? <ul className="permission-list">{session.user.permissions.map((permission) => <li key={permission}>{permission}</li>)}</ul> : <p className="muted">No permissions are assigned.</p>}</article></div>
  </section>;
}

function useDirectory(session: ReturnType<typeof useAuth>["session"]) {
  return useQuery({ queryKey: ["administration", "directory"], queryFn: () => createApiClient({ csrfToken: session?.csrf_token }).request("/users", directorySchema), enabled: Boolean(session), retry: false });
}

export function AdministrationPage() {
  const { session } = useAuth();
  const directory = useDirectory(session);
  if (directory.isPending) return <div className="loading workspace-loading" role="status"><span aria-hidden="true" /><span>Loading users and roles…</span></div>;
  if (directory.error || !directory.data) return <ErrorState error={directory.error} retry={() => directory.refetch()} />;
  return <AdministrationContent directory={directory.data} />;
}

function AdministrationContent({ directory }: { directory: Directory }) {
  const { session } = useAuth();
  const queryClient = useQueryClient();
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteRoles, setInviteRoles] = useState<string[]>([]);
  const [inviteMessage, setInviteMessage] = useState("");
  const [inviteError, setInviteError] = useState("");
  const [role, setRole] = useState<EditableRole | null>(null);
  const [roleMessage, setRoleMessage] = useState("");
  const [roleError, setRoleError] = useState("");
  const invalidate = () => Promise.all([queryClient.invalidateQueries({ queryKey: ["administration", "directory"] }), queryClient.invalidateQueries({ queryKey: ["portal"] }), queryClient.invalidateQueries({ queryKey: authSessionKey })]);
  const [userMessage, setUserMessage] = useState("");
  const [userError, setUserError] = useState("");
  const refreshAfterUnauthorized = (error: unknown) => { if (error instanceof ApiResponseError && error.status === 401) void queryClient.invalidateQueries({ queryKey: authSessionKey }); };
  const updateUser = useMutation({ mutationFn: ({ id, roles, disabled }: { id: string; roles: string[]; disabled: boolean }) => createApiClient({ csrfToken: session?.csrf_token }).request(`/users/${encodeURIComponent(id)}`, changedSchema, { method: "PUT", body: JSON.stringify({ roles, disabled }) }), onSuccess: async () => { setUserError(""); setUserMessage("Account access updated."); await invalidate(); }, onError: (e) => { refreshAfterUnauthorized(e); setUserMessage(""); setUserError(errorText(e)); } });
  const invite = useMutation({ mutationFn: () => createApiClient({ csrfToken: session?.csrf_token }).request("/users/invite", inviteSchema, { method: "POST", body: JSON.stringify({ email: inviteEmail, roles: inviteRoles }) }), onSuccess: (data) => { setInviteEmail(""); setInviteRoles([]); setInviteError(""); setInviteMessage(`Invitation queued for ${data.email}.`); void invalidate(); }, onError: (e) => { refreshAfterUnauthorized(e); setInviteMessage(""); setInviteError(errorText(e)); } });
  const saveRole = useMutation({ mutationFn: () => role ? createApiClient({ csrfToken: session?.csrf_token }).request(`/roles/${encodeURIComponent(role.id)}`, changedSchema, { method: "PUT", body: JSON.stringify({ name: role.name, permissions: role.permissions }) }) : Promise.reject(new Error("Choose a role.")), onSuccess: async () => { setRoleMessage("Role saved."); setRoleError(""); setRole((current) => current ? { ...current, isNew: false } : null); await invalidate(); }, onError: (e) => { refreshAfterUnauthorized(e); setRoleMessage(""); setRoleError(errorText(e)); } });
  const submitInvite = (event: FormEvent) => { event.preventDefault(); invite.mutate(); };
  const submitRole = (event: FormEvent) => { event.preventDefault(); if (role?.isNew && directory.roles.some((item) => item.id === role.id)) { setRoleError("That role ID already exists. Choose the role to edit it."); return; } if (role?.id && role.name) saveRole.mutate(); };
  return <section className="admin-page" aria-labelledby="admin-title"><div className="page-heading"><div><h1 id="admin-title">Users &amp; roles</h1><p>Manage workspace access. Changes sign affected users out.</p></div></div>
    <div className="admin-layout"><div className="admin-main"><article className="admin-card"><h2>Accounts <span className="row-count">{directory.users.length}</span></h2>{directory.users.length ? <div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>Email</th><th>Status</th><th>Roles</th><th>Access</th></tr></thead><tbody>{directory.users.map((user) => <UserRow key={`${user.id}:${user.disabled}:${user.roles.join(",")}`} user={user} roles={directory.roles} disabled={user.admin} onSave={(data) => updateUser.mutate({ id: user.id, ...data })} pending={updateUser.isPending} />)}</tbody></table></div> : <p className="muted">No accounts yet.</p>}{userMessage && <p className="form-success" role="status">{userMessage}</p>}{userError && <p className="field-error" role="alert">{userError}</p>}</article></div>
      <aside className="admin-rail"><article className="admin-card"><h2>Invite a person</h2><form className="stack-form" onSubmit={submitInvite}><label className="field"><span>Email</span><input type="email" required value={inviteEmail} onChange={(e) => setInviteEmail(e.target.value)} /></label><RoleChecks roles={directory.roles} selected={inviteRoles} onChange={setInviteRoles} /><button className="primary-button" disabled={invite.isPending} type="submit">{invite.isPending ? "Sending…" : "Send invitation"}</button>{inviteMessage && <p className="form-success" role="status">{inviteMessage}</p>}{inviteError && <p className="field-error" role="alert">{inviteError}</p>}</form></article><RoleEditor directory={directory} role={role} setRole={setRole} submit={submitRole} pending={saveRole.isPending} message={roleMessage} error={roleError} /></aside></div>
  </section>;
}

function UserRow({ user, roles, disabled, onSave, pending }: { user: Directory["users"][number]; roles: Directory["roles"]; disabled: boolean; onSave: (data: { roles: string[]; disabled: boolean }) => void; pending: boolean }) {
  const [selected, setSelected] = useState(user.roles);
  return <tr><td><strong>{user.email}</strong></td><td><span className={`status status-${user.disabled ? "off" : user.pending ? "pending" : "on"}`}>{user.disabled ? "Disabled" : user.pending ? "Pending" : "Active"}</span></td><td>{disabled ? <span className="role-label">Operator</span> : <select aria-label={`Roles for ${user.email}`} multiple value={selected} onChange={(e) => setSelected(Array.from(e.target.selectedOptions, (option) => option.value))}>{roles.map((role) => <option key={role.id} value={role.id}>{role.name}</option>)}{user.roles.filter((id) => !roles.some((role) => role.id === id)).map((id) => <option key={id} value={id}>{id} (legacy)</option>)}</select>}</td><td>{disabled ? <span className="muted">Read-only</span> : <><button type="button" className="table-button" disabled={pending} onClick={() => onSave({ roles: selected, disabled: !user.disabled })}>{user.disabled ? "Enable" : "Disable"}</button> <button type="button" className="table-button" disabled={pending} onClick={() => onSave({ roles: selected, disabled: user.disabled })}>Save</button></>}</td></tr>;
}

function RoleChecks({ roles, selected, onChange }: { roles: Directory["roles"]; selected: string[]; onChange: (value: string[]) => void }) { return <fieldset className="check-group"><legend>Roles</legend>{roles.length ? roles.map((role) => <label key={role.id}><input type="checkbox" checked={selected.includes(role.id)} onChange={(e) => onChange(e.target.checked ? [...selected, role.id] : selected.filter((id) => id !== role.id))} />{role.name}</label>) : <span className="muted">No roles available.</span>}</fieldset>; }

function RoleEditor({ directory, role, setRole, submit, pending, message, error }: { directory: Directory; role: EditableRole | null; setRole: (role: EditableRole | null) => void; submit: (event: FormEvent) => void; pending: boolean; message: string; error: string }) { const creating = role?.isNew; const permissions = [...new Set([...(directory.permissions), ...(role?.permissions ?? [])])]; return <article className="admin-card"><h2>Roles</h2><label className="field"><span>Role to edit</span><select value={creating ? "__new__" : role?.id || ""} onChange={(e) => { const selected = directory.roles.find((item) => item.id === e.target.value); setRole(selected ? { ...selected, isNew: false } : e.target.value === "__new__" ? { id: "", name: "", permissions: [], isNew: true } : null); }}><option value="">Choose a role</option><option value="__new__">Create a role</option>{directory.roles.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>{role && <form className="stack-form" onSubmit={submit}>{creating && <label className="field"><span>Stable ID</span><input required pattern="[a-z0-9_-]+" value={role.id} onChange={(e) => setRole({ ...role, id: e.target.value })} /></label>}<label className="field"><span>Name</span><input required value={role.name} onChange={(e) => setRole({ ...role, name: e.target.value })} /></label><fieldset className="check-group"><legend>Permissions</legend>{permissions.map((permission) => <label key={permission}><input type="checkbox" checked={role.permissions.includes(permission)} onChange={(e) => setRole({ ...role, permissions: e.target.checked ? [...role.permissions, permission] : role.permissions.filter((item) => item !== permission) })} />{permission}{!directory.permissions.includes(permission) && <span className="muted"> (legacy)</span>}</label>)}</fieldset><button className="primary-button" disabled={pending} type="submit">{pending ? "Saving…" : "Save role"}</button>{message && <p className="form-success" role="status">{message}</p>}{error && <p className="field-error" role="alert">{error}</p>}</form>}</article>; }
