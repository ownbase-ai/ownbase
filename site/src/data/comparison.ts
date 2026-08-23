/**
 * The eight-subscriptions comparison and the Your Base checklist.
 * One source of truth so the cards, the observed total, and any future
 * comparison page cannot drift.
 *
 * Prices are transcribed from the design mockup (observed May 18, 2026).
 * Confirm against current vendor list prices before treating them as live.
 */

export type SaaSCard = {
  name: string;
  examples: string;
  price: string;
  icon: string;
};

export type BaseService = {
  name: string;
  icon: string;
};

export const saasCards: SaaSCard[] = [
  {
    name: "Hosting",
    examples: "Vercel, Netlify",
    price: "$20/mo+",
    icon: "globe",
  },
  {
    name: "Database",
    examples: "Supabase, Neon",
    price: "$15/mo+",
    icon: "database",
  },
  {
    name: "Object storage",
    examples: "S3, Cloudflare R2",
    price: "$10/mo+",
    icon: "box",
  },
  {
    name: "Queue",
    examples: "Upstash, SQS",
    price: "$10/mo+",
    icon: "list",
  },
  {
    name: "Cron",
    examples: "Cron, EasyCron",
    price: "$5/mo+",
    icon: "clock",
  },
  {
    name: "Email",
    examples: "Resend, SendGrid",
    price: "$10/mo+",
    icon: "mail",
  },
  {
    name: "Auth",
    examples: "Clerk, Auth0",
    price: "$15/mo+",
    icon: "lock",
  },
  {
    name: "Logs",
    examples: "Logtail, DataDog",
    price: "$20/mo+",
    icon: "scroll-text",
  },
];

export const observedTotal = "$100–120+/mo";

export const basePlan = {
  name: "Your Base",
  machine: "Hetzner CPX41 · €29/mo",
  tagline: "One bill. One machine. Your data.",
};

export const baseServices: BaseService[] = [
  { name: "Web", icon: "globe" },
  { name: "Postgres", icon: "database" },
  { name: "Redis", icon: "layers" },
  { name: "Queue", icon: "list" },
  { name: "Cron", icon: "clock" },
  { name: "Storage", icon: "box" },
  { name: "Mail", icon: "mail" },
  { name: "Auth", icon: "lock" },
];

export const llmsUrl = "https://ownbase.ai/llms.txt";
