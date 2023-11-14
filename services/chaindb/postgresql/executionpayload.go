// Copyright © 2022, 2023 Weald Technology Trading.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package postgresql

import (
	"context"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"github.com/shopspring/decimal"
	"github.com/wealdtech/chaind/services/chaindb"
	"go.opentelemetry.io/otel"
)

// setExecutionPayload sets the execution payload of a block.
func (s *Service) setExecutionPayload(ctx context.Context, block *chaindb.Block) error {
	ctx, span := otel.Tracer("wealdtech.chaind.services.chaindb.postgresql").Start(ctx, "setExecutionPayload")
	defer span.End()

	tx := s.tx(ctx)
	if tx == nil {
		return ErrNoTransaction
	}

	if block == nil {
		return errors.New("block missing")
	}
	if block.ExecutionPayload == nil {
		// Do not treat this as an error, as pre-Bellatrix blocks will not have
		// an execution payload.
		return nil
	}
	if block.ExecutionPayload.BlockHash == [32]byte{} {
		// This is an empty execution payload, which happens after the bellatrix
		// fork but before terminal total difficulty; ignore it.
		return nil
	}

	// ExtraData can be null.
	var extraData *[]byte
	if len(block.ExecutionPayload.ExtraData) > 0 {
		extraData = &block.ExecutionPayload.ExtraData
	}

	_, err := tx.Exec(ctx, `
INSERT INTO t_block_execution_payloads(f_block_root
                                      ,f_block_number
                                      ,f_block_hash
                                      ,f_parent_hash
                                      ,f_fee_recipient
                                      ,f_state_root
                                      ,f_receipts_root
                                      ,f_logs_bloom
                                      ,f_prev_randao
                                      ,f_gas_limit
                                      ,f_gas_used
                                      ,f_base_fee_per_gas
                                      ,f_timestamp
                                      ,f_extra_data
                                      ,f_excess_data_gas
                                      )
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT (f_block_root) DO
UPDATE
SET f_block_number = excluded.f_block_number
   ,f_block_hash = excluded.f_block_hash
   ,f_parent_hash = excluded.f_parent_hash
   ,f_fee_recipient = excluded.f_fee_recipient
   ,f_state_root = excluded.f_state_root
   ,f_receipts_root = excluded.f_receipts_root
   ,f_logs_bloom = excluded.f_logs_bloom
   ,f_prev_randao = excluded.f_prev_randao
   ,f_gas_limit = excluded.f_gas_limit
   ,f_gas_used = excluded.f_gas_used
   ,f_base_fee_per_gas = excluded.f_base_fee_per_gas
   ,f_timestamp = excluded.f_timestamp
   ,f_extra_data = excluded.f_extra_data
   ,f_excess_data_gas = excluded.f_excess_data_gas
`,
		block.Root[:],
		block.ExecutionPayload.BlockNumber,
		block.ExecutionPayload.BlockHash[:],
		block.ExecutionPayload.ParentHash[:],
		block.ExecutionPayload.FeeRecipient[:],
		block.ExecutionPayload.StateRoot[:],
		block.ExecutionPayload.ReceiptsRoot[:],
		block.ExecutionPayload.LogsBloom[:],
		block.ExecutionPayload.PrevRandao[:],
		block.ExecutionPayload.GasLimit,
		block.ExecutionPayload.GasUsed,
		decimal.NewFromBigInt(block.ExecutionPayload.BaseFeePerGas, 0),
		block.ExecutionPayload.Timestamp,
		extraData,
		block.ExecutionPayload.ExcessDataGas,
	)
	if err != nil {
		return err
	}

	if err := s.setWithdrawals(ctx, block); err != nil {
		return errors.Wrap(err, "failed to set withdrawals")
	}

	return nil
}

// executionPayload fetches the execution payload of a block.
func (s *Service) executionPayload(ctx context.Context,
	tx pgx.Tx,
	root phase0.Root,
) (
	*chaindb.ExecutionPayload,
	error,
) {
	ctx, span := otel.Tracer("wealdtech.chaind.services.chaindb.postgresql").Start(ctx, "executionPayload")
	defer span.End()

	payload := &chaindb.ExecutionPayload{}
	var blockHash []byte
	var parentHash []byte
	var feeRecipient []byte
	var stateRoot []byte
	var receiptsRoot []byte
	var logsBloom []byte
	var prevRandao []byte
	var baseFeePerGas decimal.Decimal

	err := tx.QueryRow(ctx, `
SELECT f_block_number
      ,f_block_hash
      ,f_parent_hash
      ,f_fee_recipient
      ,f_state_root
      ,f_receipts_root
      ,f_logs_bloom
      ,f_prev_randao
      ,f_gas_limit
      ,f_gas_used
      ,f_base_fee_per_gas
      ,f_timestamp
      ,f_extra_data
      ,f_excess_data_gas
FROM t_block_execution_payloads
WHERE f_block_root = $1`,
		root[:],
	).Scan(
		&payload.BlockNumber,
		&blockHash,
		&parentHash,
		&feeRecipient,
		&stateRoot,
		&receiptsRoot,
		&logsBloom,
		&prevRandao,
		&payload.GasLimit,
		&payload.GasUsed,
		&baseFeePerGas,
		&payload.Timestamp,
		&payload.ExtraData,
		&payload.ExcessDataGas,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Means there is no execution payload; this is fine.
			return nil, nil
		}
		return nil, err
	}
	copy(payload.BlockHash[:], blockHash)
	copy(payload.ParentHash[:], parentHash)
	copy(payload.FeeRecipient[:], feeRecipient)
	copy(payload.StateRoot[:], stateRoot)
	copy(payload.ReceiptsRoot[:], receiptsRoot)
	copy(payload.LogsBloom[:], logsBloom)
	copy(payload.PrevRandao[:], prevRandao)
	payload.BaseFeePerGas = baseFeePerGas.BigInt()

	return payload, nil
}

// executionPayloads fetches the execution payloads of multiple blocks.
func (s *Service) executionPayloads(ctx context.Context,
	tx pgx.Tx,
	roots []phase0.Root,
) (
	map[phase0.Root]*chaindb.ExecutionPayload,
	error,
) {
	ctx, span := otel.Tracer("wealdtech.chaind.services.chaindb.postgresql").Start(ctx, "executionPayloads")
	defer span.End()

	broots := make([][]byte, len(roots))
	for i := range roots {
		broots[i] = roots[i][:]
	}

	rows, err := tx.Query(ctx, `
SELECT f_block_root
      ,f_block_number
      ,f_block_hash
      ,f_parent_hash
      ,f_fee_recipient
      ,f_state_root
      ,f_receipts_root
      ,f_logs_bloom
      ,f_prev_randao
      ,f_gas_limit
      ,f_gas_used
      ,f_base_fee_per_gas
      ,f_timestamp
      ,f_extra_data
      ,f_excess_data_gas
FROM t_block_execution_payloads
WHERE f_block_root = ANY($1)`,
		broots,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[phase0.Root]*chaindb.ExecutionPayload)
	for rows.Next() {
		payload := &chaindb.ExecutionPayload{}
		var blockRoot []byte
		var blockHash []byte
		var parentHash []byte
		var feeRecipient []byte
		var stateRoot []byte
		var receiptsRoot []byte
		var logsBloom []byte
		var prevRandao []byte
		var baseFeePerGas decimal.Decimal
		err := rows.Scan(&blockRoot,
			&payload.BlockNumber,
			&blockHash,
			&parentHash,
			&feeRecipient,
			&stateRoot,
			&receiptsRoot,
			&logsBloom,
			&prevRandao,
			&payload.GasLimit,
			&payload.GasUsed,
			&baseFeePerGas,
			&payload.Timestamp,
			&payload.ExtraData,
			&payload.ExcessDataGas,
		)
		if err != nil {
			return nil, errors.Wrap(err, "failed to scan row")
		}
		copy(payload.BlockHash[:], blockHash)
		copy(payload.ParentHash[:], parentHash)
		copy(payload.FeeRecipient[:], feeRecipient)
		copy(payload.StateRoot[:], stateRoot)
		copy(payload.ReceiptsRoot[:], receiptsRoot)
		copy(payload.LogsBloom[:], logsBloom)
		copy(payload.PrevRandao[:], prevRandao)
		payload.BaseFeePerGas = baseFeePerGas.BigInt()

		var key phase0.Root
		copy(key[:], blockRoot)
		res[key] = payload
	}

	return res, nil
}
