#!/usr/bin/env node
// mint-testnut.js — Mint a fresh Cashu test token for CI testing
//
// Basic usage (prints token to stdout):
//   node mint-testnut.js
//   node mint-testnut.js 16          # specify amount in sats
//
// No-DLEQ mode (230-byte tokens that fit in single RADIUS attribute):
//   node mint-testnut.js --no-dleq
//   node mint-testnut.js --no-dleq 8
//
// Write eapol_test config (auto-detects single-field vs split):
//   node mint-testnut.js --write-eapol-config /tmp/eapol.conf
//   node mint-testnut.js --no-dleq --write-eapol-config /tmp/eapol.conf
//   node mint-testnut.js --token-in-username --write-eapol-config /tmp/eapol.conf
//
// Write config from an existing token (no minting):
//   node mint-testnut.js --token "cashuB..." --write-eapol-config /tmp/eapol.conf

const fs = require('fs');
const path = require('path');

const MINT_URL = 'https://testnut.cashu.exchange';
const RADIUS_ATTR_LIMIT = 253;
const TOKEN_SPLIT_LEN = 200;

function parseArgs(argv) {
  const args = {
    amount: 8,
    writeEapolConfig: null,
    eapIdentity: 'ci-cashu-user',
    token: null,
    noDleq: false,
    tokenInUsername: false,
  };
  const positional = [];
  for (let i = 2; i < argv.length; i++) {
    switch (argv[i]) {
      case '--write-eapol-config': args.writeEapolConfig = argv[++i]; break;
      case '--eap-identity': args.eapIdentity = argv[++i]; break;
      case '--token': args.token = argv[++i]; break;
      case '--no-dleq': args.noDleq = true; break;
      case '--token-in-username': args.tokenInUsername = true; break;
      default: positional.push(argv[i]); break;
    }
  }
  if (positional.length > 0) args.amount = parseInt(positional[0], 10);
  return args;
}

function writeEapolConfig(filePath, token, opts) {
  const tokenLen = token.length;
  const fitsSingleField = tokenLen <= RADIUS_ATTR_LIMIT;

  let config;
  let desc;

  if (fitsSingleField && !opts.tokenInUsername) {
    // Single-field: token entirely in password, identity is a placeholder
    config = [
      'network={',
      '    ssid="cashu-ci"',
      '    key_mgmt=IEEE8021X',
      '    eap=TTLS',
      `    identity="${opts.eapIdentity}"`,
      `    password="${token}"`,
      '    phase2="auth=PAP"',
      '    anonymous_identity="anonymous"',
      '}',
    ].join('\n') + '\n';
    desc = `password-only (${tokenLen}b, fits single field)`;
  } else if (fitsSingleField && opts.tokenInUsername) {
    // Single-field: token entirely in identity/username, password is placeholder
    config = [
      'network={',
      '    ssid="cashu-ci"',
      '    key_mgmt=IEEE8021X',
      '    eap=TTLS',
      `    identity="${token}"`,
      `    password="ci-password"`,
      '    phase2="auth=PAP"',
      '    anonymous_identity="anonymous"',
      '}',
    ].join('\n') + '\n';
    desc = `identity-only (${tokenLen}b, fits single field)`;
  } else {
    // Split token: first TOKEN_SPLIT_LEN bytes in password, rest in identity
    const passwordPart = token.substring(0, TOKEN_SPLIT_LEN);
    const identityPart = token.substring(TOKEN_SPLIT_LEN);
    config = [
      'network={',
      '    ssid="cashu-ci"',
      '    key_mgmt=IEEE8021X',
      '    eap=TTLS',
      `    identity="${identityPart}"`,
      `    password="${passwordPart}"`,
      '    phase2="auth=PAP"',
      '    anonymous_identity="anonymous"',
      '}',
    ].join('\n') + '\n';
    desc = `split (password=${passwordPart.length}b, identity=${identityPart.length}b)`;
  }

  const dir = path.dirname(filePath);
  if (dir !== '.' && dir !== '/') {
    fs.mkdirSync(dir, { recursive: true });
  }
  fs.writeFileSync(filePath, config, 'utf8');
  process.stderr.write(`Wrote eapol_test config to ${filePath} (${desc})\n`);
}

async function mintToken(amount, noDleq) {
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

  if (noDleq) {
    return getEncodedToken({ mint: MINT_URL, proofs }, { removeDleq: true });
  }

  return getEncodedToken({ mint: MINT_URL, proofs });
}

async function main() {
  const args = parseArgs(process.argv);

  let token;
  if (args.token) {
    token = args.token;
    process.stderr.write(`Using provided token (${token.length} bytes)\n`);
  } else {
    token = await mintToken(args.amount, args.noDleq);
    process.stderr.write(`Minted ${args.amount} sats, token length: ${token.length}${args.noDleq ? ' (no-DLEQ)' : ' (with DLEQ)'}\n`);
  }

  if (args.writeEapolConfig) {
    writeEapolConfig(args.writeEapolConfig, token, args);
  }

  process.stdout.write(token + '\n');
}

main().catch(e => { process.stderr.write(`ERROR: ${e.message}\n`); process.exit(1); });
