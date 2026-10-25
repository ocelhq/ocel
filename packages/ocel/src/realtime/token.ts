/** A token the ocel runtime minted and the Unix second it expires at. */
export interface Token {
  token: string;
  expiresAt: number;
}

/** What a token admits its holder to: opening a socket, or subscribing to one wire channel. */
export interface TokenGrant {
  operation: "connect" | "subscribe";
  channel: string;
}

/**
 * The tokens the ocel runtime minted, one per grant in the order asked, each with the wire
 * channel it is for, and the Unix second they all expire at.
 */
export interface MintedTokens {
  tokens: { token: string; channel: string }[];
  expiresAt: number;
}
