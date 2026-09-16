package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	hostv1 "agentbox/api"
)

type CommandResult struct {
	Stage        hostv1.CommandStage
	ErrorCode    string
	ErrorMessage string
	ResultJSON   []byte
}

// CommandHandler must use HostCommand.IdempotencyKey for any external side effect.
// The client prevents duplicate deliveries from invoking HandleCommand twice while
// its durable command record exists; the key lets the handler close the remaining
// crash window between completing a side effect and recording its terminal result.
type CommandHandler interface {
	HandleCommand(ctx context.Context, command *hostv1.HostCommand) (CommandResult, error)
}

func (c *Client) dispatchCommand(ctx context.Context, command *hostv1.HostCommand) {
	if err := validateCommand(command); err != nil {
		if command != nil && command.CommandId != "" {
			ackErr := c.enqueueCommandAck(&hostv1.CommandAck{
				CommandId:    command.CommandId,
				Stage:        hostv1.CommandStage_COMMAND_STAGE_FAILED,
				ErrorCode:    "invalid_command",
				ErrorMessage: err.Error(),
			})
			if ackErr != nil {
				c.reportAsyncError(errors.Join(err, ackErr))
				return
			}
		}
		c.reportNonfatalError(err)
		return
	}

	key := command.IdempotencyKey
	if key == "" {
		key = command.CommandId
	}
	record, firstDelivery, err := c.idempotency.BeginCommand(ctx, key, command.CommandId)
	if err != nil {
		c.reportAsyncError(err)
		return
	}
	key = record.Key
	if err := c.enqueueCommandRecord(record); err != nil {
		if firstDelivery {
			if abandonErr := c.idempotency.AbandonCommand(ctx, key, command.CommandId); abandonErr != nil {
				c.reportAsyncError(errors.Join(err, abandonErr))
			} else {
				c.reportAsyncError(err)
			}
		} else {
			c.reportAsyncError(err)
		}
		return
	}
	if record.Stage != hostv1.CommandStage_COMMAND_STAGE_ACCEPTED {
		return
	}

	started, execute, err := c.idempotency.StartCommand(ctx, key, command.CommandId)
	if err != nil {
		c.reportAsyncError(err)
		return
	}
	if !execute {
		if err := c.enqueueCommandRecord(started); err != nil {
			c.reportAsyncError(err)
		}
		return
	}
	if err := c.enqueueCommandRecord(started); err != nil {
		failed := started
		failed.Stage = hostv1.CommandStage_COMMAND_STAGE_FAILED
		failed.ErrorCode = "ack_persistence_failed"
		failed.ErrorMessage = err.Error()
		if storeErr := c.idempotency.SetCommandResult(ctx, key, failed); storeErr != nil {
			c.reportAsyncError(errors.Join(err, storeErr))
		} else {
			c.reportAsyncError(err)
		}
		return
	}

	result, handlerErr := c.handler.HandleCommand(ctx, command)
	terminal := CommandRecord{
		Key:          key,
		CommandID:    command.CommandId,
		Stage:        result.Stage,
		ErrorCode:    result.ErrorCode,
		ErrorMessage: result.ErrorMessage,
		ResultJSON:   append([]byte(nil), result.ResultJSON...),
	}
	if handlerErr != nil {
		terminal.Stage = hostv1.CommandStage_COMMAND_STAGE_FAILED
		terminal.ErrorCode = "handler_error"
		terminal.ErrorMessage = handlerErr.Error()
		terminal.ResultJSON = nil
	} else if err := validateCommandResult(terminal); err != nil {
		terminal.Stage = hostv1.CommandStage_COMMAND_STAGE_FAILED
		terminal.ErrorCode = "invalid_handler_result"
		terminal.ErrorMessage = err.Error()
		terminal.ResultJSON = nil
	}
	if ctx.Err() != nil {
		terminal.Stage = hostv1.CommandStage_COMMAND_STAGE_CANCELLED
		terminal.ErrorCode = "client_stopped"
		terminal.ErrorMessage = ctx.Err().Error()
		terminal.ResultJSON = nil
	}
	if err := c.idempotency.SetCommandResult(context.WithoutCancel(ctx), key, terminal); err != nil {
		c.reportAsyncError(err)
		return
	}
	if err := c.enqueueCommandRecord(terminal); err != nil {
		c.reportAsyncError(err)
	}
}

func (c *Client) enqueueCommandRecord(record CommandRecord) error {
	return c.enqueueCommandAck(&hostv1.CommandAck{
		CommandId:    record.CommandID,
		Stage:        record.Stage,
		ErrorCode:    record.ErrorCode,
		ErrorMessage: record.ErrorMessage,
		ResultJson:   append([]byte(nil), record.ResultJSON...),
	})
}

func (c *Client) enqueueCommandAck(ack *hostv1.CommandAck) error {
	_, err := c.enqueue(&hostv1.HostFrame{
		Payload: &hostv1.HostFrame_CommandAck{CommandAck: ack},
	})
	return err
}

func validateCommand(command *hostv1.HostCommand) error {
	if command == nil {
		return protocolError("dispatch command", CodeProtocol, errors.New("command is required"))
	}
	if command.CommandId == "" {
		return protocolError("dispatch command", CodeProtocol, errors.New("command id is required"))
	}
	if command.CommandType == "" {
		return protocolError("dispatch command", CodeProtocol, errors.New("command type is required"))
	}
	if len(command.PayloadJson) > 0 && !json.Valid(command.PayloadJson) {
		return protocolError("dispatch command", CodeProtocol, errors.New("command payload is not valid JSON"))
	}
	return nil
}

func validateCommandResult(record CommandRecord) error {
	if !isTerminalStage(record.Stage) {
		return fmt.Errorf("handler returned non-terminal stage %s", record.Stage)
	}
	if len(record.ResultJSON) > 0 && !json.Valid(record.ResultJSON) {
		return errors.New("handler result is not valid JSON")
	}
	return nil
}
