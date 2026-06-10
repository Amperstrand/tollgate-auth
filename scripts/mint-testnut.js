#!/usr/bin/env node
// mint-testnut.js — Mint a fresh Cashu test token for CI testing
// Usage: node mint-testnut.js > /tmp/token.txt
// Outputs: a single line with the encoded token (cashuA... or cashuB...)

const MINT_URL = 'https://testnut.cashu.exchange';
const AMOUNT = 8;

async function main() {
  // Dynamic import for ESM
  const { Wallet, getEncodedToken, MintQuoteState } = await import('@cashu/cashu-ts');

  const wallet = new Wallet(MINT_URL);
  await wallet.loadMint();

  const quote = await wallet.createMintQuoteBolt11(AMOUNT);
  process.stderr.write(`Quote: ${quote.quote}\n`);

  // Poll until paid (testnut auto-pays dummy invoices)
  let checked = await wallet.checkMintQuoteBolt11(quote.quote);
  let tries = 0;
  while (checked.state !== MintQuoteState.PAID && tries < 30) {
    await new Promise(r => setTimeout(r, 1000));
    checked = await wallet.checkMintQuoteBolt11(quote.quote);
    tries++;
    process.stderr.write(`Poll ${tries}: state=${checked.state}\n`);
  }

  if (checked.state !== MintQuoteState.PAID) {
    process.stderr.write(`ERROR: Quote not paid after ${tries}s\n`);
    process.exit(1);
  }

  const proofs = await wallet.mintProofsBolt11(AMOUNT, quote.quote);
  const token = getEncodedToken({ mint: MINT_URL, proofs });

  // Output just the token to stdout
  process.stdout.write(token + '\n');
  process.stderr.write(`Minted ${AMOUNT} sats, token length: ${token.length}\n`);
}

main().catch(e => { process.stderr.write(`ERROR: ${e.message}\n`); process.exit(1); });
