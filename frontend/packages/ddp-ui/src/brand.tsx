export function Brand({ displayName, logo, tagline = "Client workspace" }: { displayName: string; logo: string | null; tagline?: string }) {
  return <>
    {logo ? <img className="mark brand-logo" src={logo} alt="" width={34} height={34} /> : <span className="mark" aria-hidden="true">J</span>}
    <span><strong title={displayName}>{displayName}</strong><small>{tagline}</small></span>
  </>;
}
