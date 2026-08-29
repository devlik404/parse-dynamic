/*
    Dynamic parser reader parameters for Microsoft SQL Server.

    Run this script in each destination/target database.

    Scope:
      - ParamReadFile describes how an input file must be decoded.
      - ParamReadFileColumn describes the source-file schema and data types.
      - Existing ParamParseFile and ParamParseFileMappingColumn remain the
        source of TargetDB, TargetTable, target columns, and target mappings.
      - This script does not create or alter any business target table.

    Index convention:
      - ParamReadFileColumn.Col is 1-based so it matches the existing
        ParamParseFileMappingColumn.Col value directly.
      - The application converts Col to a 0-based parser index when required.
      - FixedStart is also stored as 1-based, matching
        PARSER_FIXED_WIDTH_FIELDS=name:start:length:type.
*/

SET NOCOUNT ON;
SET XACT_ABORT ON;

BEGIN TRY
    BEGIN TRANSACTION;

    IF OBJECT_ID(N'dbo.ParamReadFile', N'U') IS NULL
    BEGIN
        CREATE TABLE dbo.ParamReadFile
        (
            ID                         BIGINT IDENTITY(1,1) NOT NULL,
            ProductID                  VARCHAR(100) NOT NULL,
            [Task]                     VARCHAR(100) NOT NULL,
            Activity                   VARCHAR(100) NOT NULL,
            IsActive                   BIT NOT NULL
                CONSTRAINT DF_ParamReadFile_IsActive DEFAULT (1),
            ConfigVersion              INT NOT NULL
                CONSTRAINT DF_ParamReadFile_ConfigVersion DEFAULT (1),

            -- PARSER_FILE_TYPE and common reader settings.
            FileType                   VARCHAR(30) NOT NULL,
            Delimiter                  NVARCHAR(32) NULL,
            HasHeader                  BIT NOT NULL
                CONSTRAINT DF_ParamReadFile_HasHeader DEFAULT (0),
            FixedWidthUnit             VARCHAR(10) NOT NULL
                CONSTRAINT DF_ParamReadFile_FixedWidthUnit DEFAULT ('BYTE'),
            JSONMode                   VARCHAR(10) NOT NULL
                CONSTRAINT DF_ParamReadFile_JSONMode DEFAULT ('AUTO'),
            JSONRecordPath             NVARCHAR(1000) NULL,
            XMLRecordPath              NVARCHAR(1000) NULL,

            -- Value parsing and normalization.
            DateFormat                 NVARCHAR(100) NOT NULL
                CONSTRAINT DF_ParamReadFile_DateFormat DEFAULT (N'2006-01-02'),
            DateTimeFormat             NVARCHAR(100) NOT NULL
                CONSTRAINT DF_ParamReadFile_DateTimeFormat
                DEFAULT (N'2006-01-02T15:04:05Z07:00'),
            Timezone                   NVARCHAR(100) NOT NULL
                CONSTRAINT DF_ParamReadFile_Timezone DEFAULT (N'UTC'),
            TrimSpace                  BIT NOT NULL
                CONSTRAINT DF_ParamReadFile_TrimSpace DEFAULT (0),
            NullIfEmpty                BIT NOT NULL
                CONSTRAINT DF_ParamReadFile_NullIfEmpty DEFAULT (0),
            AllowExtraColumns          BIT NOT NULL
                CONSTRAINT DF_ParamReadFile_AllowExtraColumns DEFAULT (0),
            SkipEmptyLine              BIT NOT NULL
                CONSTRAINT DF_ParamReadFile_SkipEmptyLine DEFAULT (1),

            -- Bounded parser limits. The application must still enforce its
            -- server-owned hard ceilings from environment configuration.
            MaxRecordBytes             INT NOT NULL
                CONSTRAINT DF_ParamReadFile_MaxRecordBytes DEFAULT (1048576),
            MaxDocumentBytes           INT NOT NULL
                CONSTRAINT DF_ParamReadFile_MaxDocumentBytes DEFAULT (67108864),
            MaxFields                  INT NOT NULL
                CONSTRAINT DF_ParamReadFile_MaxFields DEFAULT (10000),

            -- XLS/XLSX and HTML/HTM options.
            SpreadsheetSheet           NVARCHAR(255) NOT NULL
                CONSTRAINT DF_ParamReadFile_SpreadsheetSheet DEFAULT (N'0'),
            HTMLTableIndex             INT NOT NULL
                CONSTRAINT DF_ParamReadFile_HTMLTableIndex DEFAULT (0),

            -- SECTIONED_DELIMITED options.
            RecordTypeIndex            INT NOT NULL
                CONSTRAINT DF_ParamReadFile_RecordTypeIndex DEFAULT (0),
            SectionKeyIndex            INT NOT NULL
                CONSTRAINT DF_ParamReadFile_SectionKeyIndex DEFAULT (1),
            FileHeaderCode             NVARCHAR(50) NULL,
            SectionHeaderCode          NVARCHAR(50) NULL,
            DataCode                   NVARCHAR(50) NULL,
            SectionFooterCode          NVARCHAR(50) NULL,
            FileFooterCode             NVARCHAR(50) NULL,
            HeaderStartIndex           INT NOT NULL
                CONSTRAINT DF_ParamReadFile_HeaderStartIndex DEFAULT (2),
            DataStartIndex             INT NOT NULL
                CONSTRAINT DF_ParamReadFile_DataStartIndex DEFAULT (2),
            DuplicateHeaderPolicy      VARCHAR(20) NOT NULL
                CONSTRAINT DF_ParamReadFile_DuplicateHeaderPolicy DEFAULT ('ERROR'),

            -- Reserved extension point for a future decoder. Known settings
            -- above remain typed columns and take precedence in the loader.
            ExtraConfigJSON            NVARCHAR(MAX) NULL,

            CreatedAt                  DATETIME2(3) NOT NULL
                CONSTRAINT DF_ParamReadFile_CreatedAt DEFAULT (SYSUTCDATETIME()),
            UpdatedAt                  DATETIME2(3) NOT NULL
                CONSTRAINT DF_ParamReadFile_UpdatedAt DEFAULT (SYSUTCDATETIME()),

            CONSTRAINT PK_ParamReadFile PRIMARY KEY CLUSTERED (ID),
            CONSTRAINT CK_ParamReadFile_ProductID_NotBlank
                CHECK (LEN(LTRIM(RTRIM(ProductID))) > 0),
            CONSTRAINT CK_ParamReadFile_Task_NotBlank
                CHECK (LEN(LTRIM(RTRIM([Task]))) > 0),
            CONSTRAINT CK_ParamReadFile_Activity_NotBlank
                CHECK (LEN(LTRIM(RTRIM(Activity))) > 0),
            CONSTRAINT CK_ParamReadFile_ConfigVersion
                CHECK (ConfigVersion > 0),
            CONSTRAINT CK_ParamReadFile_FileType
                CHECK (FileType IN
                (
                    'DELIMITED', 'CSV', 'TSV', 'FIXED_WIDTH',
                    'JSON', 'XML', 'RAW', 'TEXT', 'HTML', 'HTM',
                    'PDF', 'XLS', 'XLSX', 'SECTIONED_DELIMITED'
                )),
            CONSTRAINT CK_ParamReadFile_FixedWidthUnit
                CHECK (FixedWidthUnit IN ('BYTE', 'RUNE')),
            CONSTRAINT CK_ParamReadFile_JSONMode
                CHECK (JSONMode IN ('AUTO', 'SINGLE', 'ARRAY', 'NDJSON')),
            CONSTRAINT CK_ParamReadFile_DuplicateHeaderPolicy
                CHECK (DuplicateHeaderPolicy IN ('ERROR', 'SUFFIX_INDEX')),
            CONSTRAINT CK_ParamReadFile_Limits
                CHECK (MaxRecordBytes > 0 AND MaxDocumentBytes > 0 AND MaxFields > 0),
            CONSTRAINT CK_ParamReadFile_Indexes
                CHECK
                (
                    HTMLTableIndex >= 0
                    AND RecordTypeIndex >= 0
                    AND SectionKeyIndex >= 0
                    AND HeaderStartIndex >= 0
                    AND DataStartIndex >= 0
                ),
            CONSTRAINT CK_ParamReadFile_ExtraConfigJSON
                CHECK (ExtraConfigJSON IS NULL OR ISJSON(ExtraConfigJSON) = 1)
        );
    END;

    IF OBJECT_ID(N'dbo.ParamReadFileColumn', N'U') IS NULL
    BEGIN
        CREATE TABLE dbo.ParamReadFileColumn
        (
            ID                         BIGINT IDENTITY(1,1) NOT NULL,
            ParamReadFileID            BIGINT NOT NULL,

            -- One-based source column number. This joins directly to the
            -- existing ParamParseFileMappingColumn.Col.
            Col                        INT NOT NULL,

            -- Canonical source name used inside ParserConfig.Columns.
            SourceName                 NVARCHAR(128) NOT NULL,

            -- Header name, JSON/XML path, RAW selector, or other decoder
            -- selector. NULL means that the loader derives it from FileType,
            -- Col, HasHeader, and SourceName.
            SourceSelector             NVARCHAR(1000) NULL,
            DataType                   VARCHAR(20) NOT NULL
                CONSTRAINT DF_ParamReadFileColumn_DataType DEFAULT ('string'),

            -- Used only by FIXED_WIDTH. FixedStart is one-based.
            FixedStart                 INT NULL,
            FixedLength                INT NULL,

            IsRequired                 BIT NOT NULL
                CONSTRAINT DF_ParamReadFileColumn_IsRequired DEFAULT (0),
            DefaultValue               NVARCHAR(4000) NULL,
            NormalizeCase              VARCHAR(10) NOT NULL
                CONSTRAINT DF_ParamReadFileColumn_NormalizeCase DEFAULT ('NONE'),

            -- JSON array of ordered operations for this SourceName, for
            -- example: [{"operation":"TRIM"},{"operation":"NULL_IF_EMPTY"}].
            TransformConfigJSON        NVARCHAR(MAX) NULL,
            IsActive                   BIT NOT NULL
                CONSTRAINT DF_ParamReadFileColumn_IsActive DEFAULT (1),
            CreatedAt                  DATETIME2(3) NOT NULL
                CONSTRAINT DF_ParamReadFileColumn_CreatedAt DEFAULT (SYSUTCDATETIME()),
            UpdatedAt                  DATETIME2(3) NOT NULL
                CONSTRAINT DF_ParamReadFileColumn_UpdatedAt DEFAULT (SYSUTCDATETIME()),

            CONSTRAINT PK_ParamReadFileColumn PRIMARY KEY CLUSTERED (ID),
            CONSTRAINT FK_ParamReadFileColumn_ParamReadFile
                FOREIGN KEY (ParamReadFileID) REFERENCES dbo.ParamReadFile(ID),
            CONSTRAINT CK_ParamReadFileColumn_Col
                CHECK (Col >= 1),
            CONSTRAINT CK_ParamReadFileColumn_SourceName_NotBlank
                CHECK (LEN(LTRIM(RTRIM(SourceName))) > 0),
            CONSTRAINT CK_ParamReadFileColumn_DataType
                CHECK (DataType IN
                (
                    'any', 'string', 'integer', 'int64', 'decimal',
                    'float', 'boolean', 'date', 'datetime', 'uuid', 'json'
                )),
            CONSTRAINT CK_ParamReadFileColumn_FixedRange
                CHECK
                (
                    (FixedStart IS NULL AND FixedLength IS NULL)
                    OR
                    (
                        FixedStart IS NOT NULL
                        AND FixedLength IS NOT NULL
                        AND FixedStart >= 1
                        AND FixedLength > 0
                    )
                ),
            CONSTRAINT CK_ParamReadFileColumn_NormalizeCase
                CHECK (NormalizeCase IN ('NONE', 'UPPER', 'LOWER')),
            CONSTRAINT CK_ParamReadFileColumn_TransformConfigJSON
                CHECK (TransformConfigJSON IS NULL OR ISJSON(TransformConfigJSON) = 1)
        );
    END;

    -- Only one active reader profile is allowed for one scheduler identity.
    IF NOT EXISTS
    (
        SELECT 1
        FROM sys.indexes
        WHERE object_id = OBJECT_ID(N'dbo.ParamReadFile')
          AND name = N'UX_ParamReadFile_ActiveIdentity'
    )
    BEGIN
        CREATE UNIQUE NONCLUSTERED INDEX UX_ParamReadFile_ActiveIdentity
            ON dbo.ParamReadFile (ProductID, [Task], Activity)
            WHERE IsActive = 1;
    END;

    IF NOT EXISTS
    (
        SELECT 1
        FROM sys.indexes
        WHERE object_id = OBJECT_ID(N'dbo.ParamReadFile')
          AND name = N'IX_ParamReadFile_Lookup'
    )
    BEGIN
        CREATE NONCLUSTERED INDEX IX_ParamReadFile_Lookup
            ON dbo.ParamReadFile (ProductID, [Task], Activity, IsActive)
            INCLUDE
            (
                ID, ConfigVersion, FileType, Delimiter, HasHeader,
                UpdatedAt
            );
    END;

    IF NOT EXISTS
    (
        SELECT 1
        FROM sys.indexes
        WHERE object_id = OBJECT_ID(N'dbo.ParamReadFileColumn')
          AND name = N'UX_ParamReadFileColumn_ActiveCol'
    )
    BEGIN
        CREATE UNIQUE NONCLUSTERED INDEX UX_ParamReadFileColumn_ActiveCol
            ON dbo.ParamReadFileColumn (ParamReadFileID, Col)
            WHERE IsActive = 1;
    END;

    IF NOT EXISTS
    (
        SELECT 1
        FROM sys.indexes
        WHERE object_id = OBJECT_ID(N'dbo.ParamReadFileColumn')
          AND name = N'UX_ParamReadFileColumn_ActiveSourceName'
    )
    BEGIN
        CREATE UNIQUE NONCLUSTERED INDEX UX_ParamReadFileColumn_ActiveSourceName
            ON dbo.ParamReadFileColumn (ParamReadFileID, SourceName)
            WHERE IsActive = 1;
    END;

    COMMIT TRANSACTION;
END TRY
BEGIN CATCH
    IF XACT_STATE() <> 0
        ROLLBACK TRANSACTION;
    THROW;
END CATCH;

/*
    Lookup used by the dynamic parser:

    SELECT TOP (1) *
    FROM dbo.ParamReadFile
    WHERE ProductID = @ProductID
      AND [Task] = @Task
      AND Activity = @Activity
      AND IsActive = 1
    ORDER BY ConfigVersion DESC, ID DESC;

    SELECT *
    FROM dbo.ParamReadFileColumn
    WHERE ParamReadFileID = @ParamReadFileID
      AND IsActive = 1
    ORDER BY Col;

    Example DELIMITED profile (execute only after replacing the example keys):

    DECLARE @ParamReadFileID BIGINT;

    INSERT dbo.ParamReadFile
    (
        ProductID, [Task], Activity, FileType, Delimiter,
        HasHeader, TrimSpace, SkipEmptyLine
    )
    VALUES
    (
        'PRODUCT_ID', 'TASK_NAME', 'ACTIVITY_NAME', 'DELIMITED', N'|',
        0, 1, 1
    );

    SET @ParamReadFileID = SCOPE_IDENTITY();

    INSERT dbo.ParamReadFileColumn
    (
        ParamReadFileID, Col, SourceName, SourceSelector, DataType, IsRequired
    )
    VALUES
        (@ParamReadFileID, 1, N'transaction_id', NULL, 'string', 1),
        (@ParamReadFileID, 2, N'transaction_date', NULL, 'date', 1),
        (@ParamReadFileID, 3, N'amount', NULL, 'decimal', 1);

    Existing target mapping remains unchanged:

        ParamReadFileColumn.Col
            -> ParamParseFileMappingColumn.Col
            -> ParamParseFileMappingColumn.FieldTargetName
*/
