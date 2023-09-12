// JEV: create index.ts file in helpers folder
import { CalculateFieldHelper } from './CalculateFieldHelper';
import { ConcatenateHelper } from './ConcatenateHelper';
import { ConfigFromQueryHelper } from './ConfigFromQueryHelper';
import { ConvertFieldTypeHelper } from './ConvertFieldTypeHelper';
import { CreateHeatmapHelp } from './CreateHeatmapHelp';
import { ExtractFieldsHelper } from './ExtractFieldsHelper';
import { FieldLookupHelper } from './FieldLookupHelper';
import { FilterByRefIdHelper } from './FilterByRefIdHelper';
import { FilterByValueHelper } from './FilterByValueHelper';
import { FilterFieldsByNameHelper } from './FilterFieldsByNameHelper';
import { FormatTimeHelper } from './FormatTimeHelper';
import { GroupByHelper } from './GroupByHelper';
import { GroupingToMatrixHelper } from './GroupingToMatrixHelper';
import { HistogramHelper } from './HistogramHelper';
import { JoinByFieldHelper } from './JoinByFieldHelper';
import { JoinByLabelsHelper } from './JoinByLabelsHelper';
import { LabelsToFieldsHelper } from './LabelsToFieldsHelper';
import { LimitHelper } from './LimitHelper';
import { MergeHelper } from './MergeHelper';
import { OrganizeFieldsHelper } from './OrganizeFieldsHelper';
import { PartitionByValuesHelper } from './PartitionByValuesHelper';
import { PrepareTimeSeriesHelper } from './PrepareTimeSeriesHelper';
import { ReduceHelper } from './ReduceHelper';
import { RenameByRegexHelper } from './RenameByRegexHelper';
import { RowsToFieldsHelper } from './RowsToFieldsHelper';
import { SeriesToRowsHelper } from './SeriesToRowsHelper';
import { SortByHelper } from './SortByHelper';
import { SpatialHelper } from './SpatialHelper';

interface Helper {
  [key: string]: string;
}

const helperContent: Helper = {
  calculateField: CalculateFieldHelper(),
  concatenate: ConcatenateHelper(),
  configFromData: ConfigFromQueryHelper(),
  convertFieldType: ConvertFieldTypeHelper(),
  extractFields: ExtractFieldsHelper(),
  fieldLookup: FieldLookupHelper(),
  filterByRefId: FilterByRefIdHelper(),
  filterByValue: FilterByValueHelper(),
  filterFieldsByName: FilterFieldsByNameHelper(),
  formatTime: FormatTimeHelper(),
  groupBy: GroupByHelper(),
  groupingToMatrix: GroupingToMatrixHelper(),
  heatmap: CreateHeatmapHelp(),
  histogram: HistogramHelper(),
  joinByField: JoinByFieldHelper(),
  joinByLabels: JoinByLabelsHelper(),
  labelsToFields: LabelsToFieldsHelper(),
  limit: LimitHelper(),
  merge: MergeHelper(),
  organize: OrganizeFieldsHelper(),
  partitionByValues: PartitionByValuesHelper(),
  prepareTimeSeries: PrepareTimeSeriesHelper(),
  reduce: ReduceHelper(),
  renameByRegex: RenameByRegexHelper(),
  rowsToFields: RowsToFieldsHelper(),
  seriesToRows: SeriesToRowsHelper(),
  sortBy: SortByHelper(),
  spatial: SpatialHelper(),
};

// JEV: add logic for no helper/string content
export function getHelperContent(id: string): string {
  // JEV: why is this never being displayed?
  const defaultMessage = 'u broke it, u buy it';

  if (id in helperContent) {
    return helperContent[id];
  }

  return defaultMessage;
}
