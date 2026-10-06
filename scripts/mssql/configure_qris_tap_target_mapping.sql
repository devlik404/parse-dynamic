/*
    Jalankan pada SQL Server ParamEngine.

    FileName dan FileDate tidak dimasukkan di sini; keduanya berasal dari
    ParamParseFile.FieldFilename dan ParamParseFile.FieldFiledate.
*/

SET NOCOUNT ON;
SET XACT_ABORT ON;

DECLARE @ProductID VARCHAR(100) = 'QRIS_TAP';
DECLARE @Task VARCHAR(100) = 'PARSE_FILE';
DECLARE @Activity VARCHAR(100) = 'PARSE';

INSERT dbo.ParamParseFileMappingColumn
(
    ProductId, [Task], Activity, Col, FieldTargetName, FixedColSubstring
)
SELECT @ProductID, @Task, @Activity, source.Col, source.FieldTargetName, ''
FROM
(
    VALUES
        ( 1, 'terminal_id'),
        ( 2, 'retrieval_reference_number'),
        ( 3, 'merchant_beneficiary_pan'),
        ( 4, 'transaction_date'),
        ( 5, 'transaction_time'),
        ( 6, 'processing_code'),
        ( 7, 'transaction_amount'),
        ( 8, 'transaction_amount_currency'),
        ( 9, 'merchant_type'),
        (10, 'merchant_criteria'),
        (11, 'beneficiary_bank_code'),
        (12, 'issuer_bank_code'),
        (13, 'forwarding_institution_id'),
        (14, 'response_code'),
        (15, 'customer_pan'),
        (16, 'invoice_data'),
        (17, 'approval_code'),
        (18, 'message_type_indicator'),
        (19, 'product_indicator'),
        (20, 'beneficiary_account'),
        (21, 'transaction_indicator')
) AS source (Col, FieldTargetName)
WHERE NOT EXISTS
(
    SELECT 1
    FROM dbo.ParamParseFileMappingColumn existing
    WHERE existing.ProductId = @ProductID
      AND existing.[Task] = @Task
      AND existing.Activity = @Activity
      AND existing.Col = source.Col
);
