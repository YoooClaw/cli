export function previewText(text: string | undefined, max = 200): string {
  if (!text) return "";
  return text.length <= max ? text : `${text.substring(0, max)}…`;
}

export function maskSecret(value: string | undefined): string {
  if (!value) return "empty";
  if (value.length <= 8) {
    return `${value.slice(0, 2)}...${value.slice(-2)}`;
  }
  return `${value.slice(0, 4)}...${value.slice(-4)}`;
}

export function redactUrlSecrets(rawUrl: string): string {
  try {
    const url = new URL(rawUrl);
    for (const key of ["apiKey", "token", "access_token"]) {
      const value = url.searchParams.get(key);
      if (value) {
        url.searchParams.set(key, maskSecret(value));
      }
    }
    return url.toString();
  } catch {
    return rawUrl.replace(
      /([?&](?:apiKey|token|access_token)=)([^&]+)/gi,
      (_match, prefix: string, value: string) => {
        let decoded = value;
        try {
          decoded = decodeURIComponent(value);
        } catch {
          // Keep the original value when the URL is malformed.
        }
        return `${prefix}${maskSecret(decoded)}`;
      },
    );
  }
}

export function findHeaderValue(
  headers: Record<string, string> | undefined,
  key: string,
): string | undefined {
  if (!headers) return undefined;
  const lowerKey = key.toLowerCase();
  for (const [headerKey, headerValue] of Object.entries(headers)) {
    if (headerKey.toLowerCase() === lowerKey) {
      return headerValue;
    }
  }
  return undefined;
}

export function summarizeRequestHeaders(headers: Record<string, string> | undefined): string {
  const contentType = findHeaderValue(headers, "content-type");
  const requestId = findHeaderValue(headers, "x-request-id");
  const parts: string[] = [];
  if (contentType) {
    parts.push(`contentType=${contentType}`);
  }
  if (requestId) {
    parts.push(`xRequestId=${previewText(requestId, 120)}`);
  }
  return parts.length ? `, ${parts.join(", ")}` : "";
}
