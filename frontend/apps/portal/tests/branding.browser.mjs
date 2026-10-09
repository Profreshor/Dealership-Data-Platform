import { chromium, expect } from '@playwright/test';
const browser = await chromium.launch();
try {
  const backgrounds=[];
  for (const mode of ['dark','light']) {
    const context=await browser.newContext({baseURL:process.env.DDP_BASE_URL,colorScheme:mode,viewport:{width:390,height:844}});
    const page=await context.newPage();
    const errors=[];
    page.on('pageerror',(error)=>errors.push(error.message));
    page.on('console',(message)=>{if(message.type()==='error') errors.push(message.text());});
    await page.goto('/login');
    await expect(page).toHaveTitle('Synthetic Blue');
    await expect(page.getByRole('link',{name:'Synthetic Blue home'})).toBeVisible();
    const logo=page.locator('.brand-logo');
    await expect(logo).toHaveAttribute('src','/logo.svg');
    await expect.poll(()=>logo.evaluate((image)=>image.complete && image.naturalWidth>0)).toBe(true);
    const branding=await page.evaluate(()=>{
      const style=getComputedStyle(document.documentElement);
      return {background:style.backgroundColor,color:style.color,scheme:style.colorScheme,accent:style.getPropertyValue('--accent').trim()};
    });
    expect(branding.scheme).toBe(mode);
    expect(branding.color).not.toBe(branding.background);
    expect(branding.accent).toMatch(/^#[0-9a-f]{6}$/);
    expect(branding.accent).not.toBe('#67c7aa');
    backgrounds.push(branding.background);
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await page.screenshot({path:`/tmp/ddp-branding-${mode}-mobile-login.png`,fullPage:true});
    await page.getByLabel('Email',{exact:true}).fill('manager@example.test');
    await page.getByLabel('Password',{exact:true}).fill('manager-password');
    await page.getByRole('button',{name:'Sign in',exact:true}).click();
    await expect(page.locator('.app-shell')).toBeVisible();
    await page.setViewportSize({width:1440,height:1000});
    await page.goto('/customers');
    await expect(page.getByRole('cell',{name:'Synthetic Customer',exact:true})).toBeVisible();
    await expect(page.getByRole('link',{name:'Synthetic Blue home'})).toBeVisible();
    await expect.poll(()=>page.locator('.brand-logo').evaluate((image)=>image.complete && image.naturalWidth>0)).toBe(true);
    await page.screenshot({path:`/tmp/ddp-branding-${mode}-desktop-table.png`,fullPage:true});
    for(const path of ['/profile','/admin/users']) {
      await page.goto(path);
      await expect(page.locator('.admin-card').first()).toBeVisible();
      expect(await page.locator('.admin-card').first().evaluate((element)=>getComputedStyle(element).color!==getComputedStyle(element).backgroundColor)).toBe(true);
    }
    await page.getByLabel('Account menu').click();
    await page.getByRole('button',{name:'Sign out',exact:true}).click();
    await expect(page.getByRole('heading',{name:'Sign in',exact:true})).toBeVisible();
    await page.getByLabel('Email',{exact:true}).fill('admin@example.test');
    await page.getByLabel('Password',{exact:true}).fill('admin-password');
    await page.getByRole('button',{name:'Sign in',exact:true}).click();
    await expect(page.locator('.app-shell')).toBeVisible();
    await page.goto('/system');
    await expect(page.locator('.system-console')).toBeVisible();
    expect(await page.locator('.system-console').evaluate((element)=>getComputedStyle(element).color!==getComputedStyle(document.documentElement).backgroundColor)).toBe(true);
    expect(errors.filter((message)=>!message.includes('401 (Unauthorized)'))).toEqual([]);
    await context.close();
  }
  expect(backgrounds[0]).not.toBe(backgrounds[1]);
} finally {await browser.close();}
