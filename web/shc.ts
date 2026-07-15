/**
 * SHC (Sovereign Hybrid Compute) User API client.
 *
 * TypeScript port of shc_toolkit/client.py, updated for API v2.4.0+.
 * Base URL: https://blesta.sovereignhybridcompute.com/user-api/v2
 * Auth: Authorization: Bearer {SHC_API_KEY}
 *
 * v2.4.0 changes adopted:
 * - error_code field (stable machine codes: not_found, vm_locked, etc.)
 * - retry_after_seconds on rate-limited/transient errors
 * - Responses start with '{' (no plaintext prefix strip needed)
 * - Confirmation flow uses error_code check
 */

const SHC_BASE_URL = "https://blesta.sovereignhybridcompute.com/user-api/v2";


// ── Types ──────────────────────────────────────────────────

export interface SHCErrorData {
  code: string;
  message: string;
  request_id?: string;
  details?: unknown;
  error_code?: string;
  retry_after_seconds?: number;
}

export class SHCError extends Error {
  readonly code: string;
  readonly errorCode?: string;
  readonly requestId?: string;
  readonly details?: unknown;
  readonly confirmationId?: string;
  readonly retryAfterSeconds?: number;

  constructor(data: SHCErrorData, confirmationId?: string) {
    const label = data.error_code ?? data.code;
    const retry = data.retry_after_seconds ? ` retry_after=${data.retry_after_seconds}s` : "";
    super(`[${label}] ${data.message}${retry}`);
    this.name = "SHCError";
    this.code = data.code;
    this.errorCode = data.error_code;
    this.requestId = data.request_id;
    this.details = data.details;
    this.confirmationId = confirmationId;
    this.retryAfterSeconds = data.retry_after_seconds;
  }
}

interface SHCResponseBody {
  data?: unknown;
  error?: {
    code: string;
    message: string;
    request_id?: string;
    details?: unknown;
    error_code?: string;
    retry_after_seconds?: number;
  };
  confirmation?: { structuredContent?: { confirmation_id?: string } };
}

interface AddCreditResult {
  status: string;
  type: string;
  amount: string;
  currency: string;
  checkout_url: string;
  bolt11: string | null;
  payment_link?: string | null;
  expires_at?: string | null;
  [key: string]: unknown;
}

interface SubmitOrderResult {
  invoice?: Record<string, unknown>;
  service_ids: number[];
  [key: string]: unknown;
}

export interface VMInfo {
  ips?: { ip: string; [key: string]: unknown }[];
  hostname?: string;
  os_user?: string;
  service_status?: string;
  provisioning_state?: string;
  [key: string]: unknown;
}

export interface SHCClientOptions {
  maxRetries?: number;
  backoffBase?: number;
  backoffCap?: number;
}

// ── Client ─────────────────────────────────────────────────

export class SHCClient {
  private readonly apiKey: string;
  private readonly baseUrl: string;
  private readonly maxRetries: number;
  private readonly backoffBase: number;
  private readonly backoffCap: number;

  constructor(apiKey: string, baseUrl: string = SHC_BASE_URL, opts?: SHCClientOptions) {
    if (!apiKey) throw new Error("SHC_API_KEY is required");
    this.apiKey = apiKey;
    this.baseUrl = baseUrl;
    this.maxRetries = opts?.maxRetries ?? 3;
    this.backoffBase = opts?.backoffBase ?? 1.0;
    this.backoffCap = opts?.backoffCap ?? 60.0;
  }

  private backoffDelay(attempt: number): number {
    const delay = Math.min(this.backoffCap, this.backoffBase * (2 ** attempt));
    const jitter = delay * 0.2 * (Math.random() * 2 - 1);
    return Math.min(this.backoffCap, Math.max(0, delay + jitter));
  }

  private async sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }

  // ── Core request ──────────────────────────────────────────

  private async request(
    method: string,
    path: string,
    body?: unknown,
    headers?: Record<string, string>,
  ): Promise<unknown> {
    const url = `${this.baseUrl}${path}`;
    const init: RequestInit = {
      method,
      headers: {
        Authorization: `Bearer ${this.apiKey}`,
        ...headers,
      },
    };

    if (body !== undefined) {
      (init.headers as Record<string, string>)["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }

    const resp = await fetch(url, init);
    const rawText = await resp.text();
    const jsonBody = (rawText.trim().startsWith("{") ? JSON.parse(rawText) : {}) as SHCResponseBody;

    if (!resp.ok) {
      const err = jsonBody.error ?? {
        code: "unknown",
        message: `HTTP ${resp.status}`,
      };
      const conf = jsonBody.confirmation;
      const confirmationId = conf?.structuredContent?.confirmation_id;
      throw new SHCError(
        {
          code: err.code ?? "unknown",
          message: err.message ?? `HTTP ${resp.status}`,
          request_id: err.request_id,
          details: err.details,
          error_code: err.error_code,
          retry_after_seconds: err.retry_after_seconds,
        },
        confirmationId,
      );
    }

    return jsonBody.data ?? jsonBody;
  }

  private async requestWithRetry(
    method: string,
    path: string,
    body?: unknown,
    headers?: Record<string, string>,
  ): Promise<unknown> {
    let lastError: unknown;
    for (let attempt = 0; attempt < this.maxRetries; attempt++) {
      try {
        return await this.request(method, path, body, headers);
      } catch (e) {
        lastError = e;
        if (!(e instanceof SHCError)) throw e;

        const retryable = e.errorCode === "rate_limited" || e.errorCode === "service_unavailable";
        if (!retryable && e.code !== "confirmation_required") {
          // Non-retryable error — but confirmation_required is handled by confirmedRequest
          throw e;
        }
        if (attempt < this.maxRetries - 1) {
          const delay = e.retryAfterSeconds ?? this.backoffDelay(attempt);
          await this.sleep(delay * 1000);
          continue;
        }
      }
    }
    throw lastError;
  }

  /**
   * Execute a request, auto-handling the confirmation_required 409 flow.
   */
  private async confirmedRequest(
    method: string,
    path: string,
    body?: unknown,
    headers?: Record<string, string>,
  ): Promise<unknown> {
    try {
      return await this.requestWithRetry(method, path, body, headers);
    } catch (e) {
      if (!(e instanceof SHCError)) throw e;
      const isConfirmation = e.errorCode === "confirmation_required" || e.code === "confirmation_required";
      if (!isConfirmation || !e.confirmationId) throw e;
      return await this.requestWithRetry(method, path, body, {
        ...headers,
        "X-User-Api-Confirm": e.confirmationId,
      });
    }
  }

  // ── Billing ───────────────────────────────────────────────

  async addCredit(amount: string, currency: string, idempotencyKey: string): Promise<AddCreditResult> {
    const data = await this.confirmedRequest(
      "POST",
      "/account/credit",
      { amount, currency, idempotency_key: idempotencyKey },
    );
    return data as AddCreditResult;
  }

  async getBalance(): Promise<number> {
    const data = await this.request("GET", "/account/balance");
    const credits = (data as Record<string, unknown>)?.credit as Array<Record<string, string>> ?? [];
    const usd = credits.find((c) => c.currency === "USD");
    return usd ? parseFloat(usd.amount) : 0;
  }

  // ── Ordering ──────────────────────────────────────────────

  async submitOrder(
    idempotencyKey: string,
    hostname: string,
    packageId: number,
    pricingId: number,
    configOptions?: Record<string, string>,
  ): Promise<SubmitOrderResult> {
    const body: Record<string, unknown> = {
      hostname,
      package_id: packageId,
      pricing_id: pricingId,
      order_form_id: 11,
      config_options: configOptions ?? { "126": "debian13-cloud" },
    };
    const data = await this.confirmedRequest(
      "POST",
      "/ordering/submit",
      body,
      { "Idempotency-Key": idempotencyKey },
    );
    return data as SubmitOrderResult;
  }

  // ── VM Lifecycle ──────────────────────────────────────────

  async getVM(serviceId: number): Promise<VMInfo> {
    const data = await this.request("GET", `/vm/${serviceId}`);
    return data as VMInfo;
  }

  async cancelVMEndOfTerm(serviceId: number): Promise<unknown> {
    return await this.confirmedRequest("POST", `/vm/${serviceId}/cancel`, {});
  }

  async cancelVMImmediate(serviceId: number): Promise<unknown> {
    return await this.confirmedRequest("POST", `/vm/${serviceId}/cancel`, { immediate: true });
  }

  async applySSHKeyLive(serviceId: number, sshKey: string): Promise<unknown> {
    return await this.confirmedRequest(
      "POST",
      `/vm/${serviceId}/ssh-keys/apply-live`,
      { ssh_key: sshKey },
    );
  }

  async getVMDetail(serviceId: number): Promise<VMInfo & Record<string, unknown>> {
    const data = await this.request("GET", `/vm/${serviceId}/detail`);
    return data as VMInfo & Record<string, unknown>;
  }

  async getVMCredentials(serviceId: number): Promise<{ user: string; password: string }> {
    const data = await this.request("GET", `/vm/${serviceId}/credentials`);
    return data as { user: string; password: string };
  }

  async getVMSummary(serviceId: number): Promise<VMInfo & Record<string, unknown>> {
    const data = await this.request("GET", `/vm/${serviceId}/summary`);
    return data as VMInfo & Record<string, unknown>;
  }

  async listVMs(): Promise<VMInfo[]> {
    const data = await this.request("GET", "/vm");
    return (data as { items?: VMInfo[] })?.items ?? [];
  }
}
