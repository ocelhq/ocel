/** A token the ocel runtime minted and the Unix second it expires at. */
export interface Token {
  token: string;
  expiresAt: number;
}

/**
 * The tokens the ocel runtime minted for one request: the connect token when one was asked
 * for, a subscribe token for each channel asked for, in the order asked, with the wire
 * channel it is for, and the Unix second they all expire at.
 */
export interface MintedTokens {
  connectToken?: string;
  subscribeTokens: { token: string; channel: string }[];
  expiresAt: number;
}
