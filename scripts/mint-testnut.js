#!/usr/bin/env node
// mint-testnut.js — Mint a fresh Cashu test token for CI testing
//
// Basic usage (prints token to stdout):
//   node mint-testnut.js
//   node mint-testnut.js 16          # specify amount in sats
//
// Write eapol_test config (token never touches shell):
//   node mint-testnut.js --write-eapol-config /tmp/eapol.conf --eap-identity ci-user
//
// Write config from an existing token (no minting):
//   node mint-testnut.js --token "cashuB..." --write-eapol-config /tmp/eapol.conf --eap-identity ci-user

const fs = require('fs');
const path = require('path');

const MINT_URL = 'https://testnut.cashu.exchange';

function parseArgs(argv) {
  const args = { amount: 8, writeEapolConfig: null, eapIdentity: 'ci-cashu-user', token: null };
  const positional = [];
  for (let i = 2; i < argv.length; i++) {
    switch (argv[i]) {
      case '--write-eapol-config': args.writeEapolConfig = argv[++i]; break;
      case '--eap-identity': args.eapIdentity = argv[++i]; break;
      case '--token': args.token = argv[++i]; break;
      default: positional.push(argv[i]); break;
    }
  }
  if (positional.length > 0) args.amount = parseInt(positional[0], 10);
  return args;
}

function writeEapolConfig(filePath, token, identity) {
  const config = [
    'network={',
    '    ssid="cashu-ci"',
    '    key_mgmt=IEEE8021X',
    '    eap=TTLS',
    `    identity="${identity}"`,
    `    password="${token}"`,
    '    phase2="auth=PAP"',
    '    anonymous_identity="anonymous"',
    '}',
  ].join('\n') + '\n';

  const dir = path.dirname(filePath);
  if (dir !== '.' && dir !== '/') {
    fs.mkdirSync(dir, { recursive: true });
  }
  fs.writeFileSync(filePath, config, 'utf8');
  process.stderr.write(`Wrote eapol_test config to ${filePath} (${config.length} bytes)\n`);
}

async function mintToken(amount) {
  const { Wallet, getEncodedToken, MintQuoteState } = await import('@cashu/cashu-ts');

  const wallet = new Wallet(MINT_URL);
  await wallet.loadMint();

  const quote = await wallet.createMintQuoteBolt11(amount);
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

  const proofs = await wallet.mintProofsBolt11(amount, quote.quote);
  return getEncodedToken({ mint: MINT_URL, proofs });
}

async function main() {
  const args = parseArgs(process.argv);

  let token;
  if (args.token) {
    token = args.token;
    process.stderr.write(`Using provided token (${token.length} bytes)\n`);
  } else {
    token = await mintToken(args.amount);
    process.stderr.write(`Minted ${args.amount} sats, token length: ${token.length}\n`);
  }

  if (args.writeEapolConfig) {
    writeEapolConfig(args.writeEapolConfig, token, args.eapIdentity);
  }

  process.stdout.write(token + '\n');
}

main().catch(e => { process.stderr.write(`ERROR: ${e.message}\n`); process.exit(1); });
