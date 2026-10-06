/*
    Contoh registrasi format QRIS TAP RH/DH/RT.

    Bagian ParamReadFile* dijalankan pada target reconciliation database.
    ParamParseFileMappingColumn tetap berada pada ParamEngine dan disediakan
    pada file configure_qris_tap_target_mapping.sql.

    Tidak ada kode record atau nama produk dari file ini yang di-hardcode di Go.
*/

SET NOCOUNT ON;
SET XACT_ABORT ON;

DECLARE @ProductID VARCHAR(100) = 'QRIS_TAP';
DECLARE @Task VARCHAR(100) = 'PARSE_FILE';
DECLARE @Activity VARCHAR(100) = 'PARSE';
DECLARE @ParamReadFileID BIGINT;

BEGIN TRY
    BEGIN TRANSACTION;

    IF EXISTS
    (
        SELECT 1
        FROM dbo.ParamReadFile
        WHERE ProductID = @ProductID
          AND [Task] = @Task
          AND Activity = @Activity
          AND IsActive = 1
    )
    BEGIN
        THROW 51000, 'Active QRIS TAP reader configuration already exists', 1;
    END;

    INSERT dbo.ParamReadFile
    (
        ProductID, [Task], Activity, IsActive, ConfigVersion,
        FileType, Delimiter, HasHeader, DateFormat, TimeFormat,
        DateTimeFormat, Timezone, TrimSpace, NullIfEmpty,
        AllowExtraColumns, AllowMissingColumns, SkipEmptyLine,
        MaxRecordBytes, MaxDocumentBytes, MaxFields,
        RecordTypeIndex, FileHeaderCode, SectionHeaderCode,
        DataCode, SectionFooterCode, FileFooterCode,
        HeaderStartIndex, DataStartIndex, DuplicateHeaderPolicy
    )
    VALUES
    (
        @ProductID, @Task, @Activity, 1, 1,
        'DELIMITED', N'|', 0, N'20060102', N'150405',
        N'2006-01-02T15:04:05Z07:00', N'Asia/Jakarta', 1, 1,
        1, 1, 1,
        1048576, 67108864, 100,
        0, N'RH', NULL,
        N'DH', NULL, N'RT',
        1, 1, 'ERROR'
    );

    SET @ParamReadFileID = SCOPE_IDENTITY();

    INSERT dbo.ParamReadFileColumn
    (
        ParamReadFileID, Col, SourceName, SourceSelector, DataType,
        IsRequired, NormalizeCase, TransformConfigJSON, IsActive
    )
    VALUES
        (@ParamReadFileID,  1, N'terminal_id',                  NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID,  2, N'retrieval_reference_number',  NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID,  3, N'merchant_beneficiary_pan',    NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID,  4, N'transaction_date',            NULL, 'date',    0, 'NONE', NULL, 1),
        (@ParamReadFileID,  5, N'transaction_time',            NULL, 'time',    0, 'NONE', NULL, 1),
        (@ParamReadFileID,  6, N'processing_code',             NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID,  7, N'transaction_amount',          NULL, 'decimal', 0, 'NONE', NULL, 1),
        (@ParamReadFileID,  8, N'transaction_amount_currency', NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID,  9, N'merchant_type',               NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 10, N'merchant_criteria',           NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 11, N'beneficiary_bank_code',       NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 12, N'issuer_bank_code',            NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 13, N'forwarding_institution_id',   NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 14, N'response_code',               NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 15, N'customer_pan',                NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 16, N'invoice_data',                NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 17, N'approval_code',               NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 18, N'message_type_indicator',      NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 19, N'product_indicator',           NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 20, N'beneficiary_account',         NULL, 'string',  0, 'NONE', NULL, 1),
        (@ParamReadFileID, 21, N'transaction_indicator',       NULL, 'string',  0, 'NONE', NULL, 1);

    COMMIT TRANSACTION;
END TRY
BEGIN CATCH
    IF XACT_STATE() <> 0
        ROLLBACK TRANSACTION;
    THROW;
END CATCH;

/*
    Mapping target dijalankan terpisah pada ParamEngine karena target DB dapat
    berada pada SQL Server yang berbeda:
    configure_qris_tap_target_mapping.sql
*/
