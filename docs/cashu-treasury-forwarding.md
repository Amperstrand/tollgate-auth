# Cashu Treasury Forwarding

## Overview

When running multiple tollgate gateways, each redeems Cashu tokens locally but needs to forward revenue to a central treasury. This document describes how forwarding works and when to use it vs wallet seed backup.

## Wallet Architecture

Each gateway runs cdk-cli with its own wallet at `/var/lib/cashu-wallet/`. Tokens are redeemed to this local wallet (NUT-03 swap invalidates client's originals). The wallet seed proves ownership of unspent tokens at the mint. If the server dies, importing the seed on a new server recovers all funds.

## When Seed Backup Is Sufficient

For a single gateway, the mint holds the cryptographic state. The seed is the backup. No forwarding needed.

## When Forwarding Adds Value

Forwarding adds value when you have multiple gateways, need automated accounting, want cold storage sweeps, consolidate across multiple mints, or need automated Lightning cash-out.

## Forwarding to lnforward

You can integrate with the lnforward service (Cloudflare Worker with Cashu ecash backend) by following these steps:

1. Gateway redeems client token via cdk-cli (NUT-03 swap)
2. Periodic sweep: cdk-cli send creates a new token from the gateway wallet
3. POST the token to lnforward's `/wallet/receive` endpoint
4. lnforward imports the token (another mint swap)
5. Tokens now held centrally in lnforward's wallet

The rotation already happened during step 1. The token forwarded in step 3 is a fresh proof, the client's original is dead. No trust issue.

## No-Rotation Option

If you trust the gateway and lnforward, the gateway could skip the cdk-cli send step and instead export proofs directly. This requires sharing the wallet seed between gateway and lnforward. Simpler but less secure. A single key compromise affects both services.

## Lightning Sweep Alternative

Instead of forwarding Cashu tokens, you can melt to Lightning:

1. cdk-cli melt creates a BOLT11 invoice paid from the gateway wallet
2. Payment goes to lnforward's Lightning Address
3. lnforward receives as new Cashu via mint quote
4. Immediate settlement, clean chain, but requires Lightning liquidity and routing fees

## Settlement Architecture

The ledger enables per-AP revenue splitting through the following process:

1. Gateway ledger records nas_id + amount_sat for each auth
2. tollgate-settle groups revenue by nas_id
3. Each AP's share is sent via NIP-17 gift-wrap DM
4. Gateway keeps configurable percentage (e.g., 20%)
5. APs receive their share (e.g., 80%) as Cashu tokens or Lightning

## Integration with tollgate-settle

The existing tollgate-settle tool (cmd/tollgate-settle/) already sends NIP-17 settlement DMs. Extending it to also forward tokens to lnforward is a small addition, add a `--forward-url` flag that POSTs tokens to lnforward's `/wallet/receive` after calculating each AP's share.