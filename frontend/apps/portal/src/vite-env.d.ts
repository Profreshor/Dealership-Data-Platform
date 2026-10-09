/// <reference types="vite/client" />

declare module "virtual:ddp-branding" {
  const branding: { display_name: string; logo: string | null };
  export default branding;
}
